package view

import (
	"bytes"
	"metaflow/querier/common"
	"time"
)

/*
	对外接口：
		struct：
			Model 包含withs tags filters等结构用于构造view
			View  由Model生成，用于构造df-clickhouse-sql
		func：
			NewModel() Model          初始化Model结构
			Model.AddTag()
			Model.AddTable()
			Model.AddGroup()
			Model.AddFilter()
			NewView(*Model) View      使用model初始化View结构
			NewView.ToString() string 生成df-clickhouse-sql
*/
type Model struct {
	Time    *Time
	Tags    *Tags
	Filters *Filters
	From    *Tables
	Groups  *Groups
	Havings *Filters
	Orders  *Orders
	Limit   *Limit
	//Havings Havings
	MetricsLevelFlag int //Metrics是否需要拆层的标识
}

func NewModel() *Model {
	return &Model{
		Time:    NewTime(),
		Tags:    &Tags{},
		Groups:  &Groups{},
		From:    &Tables{},
		Filters: &Filters{},
		Havings: &Filters{},
		Orders:  &Orders{},
		Limit:   &Limit{},
	}
}

func (m *Model) AddTag(n Node) {
	m.Tags.Append(n)
}

func (m *Model) AddFilter(f *Filters) {
	m.Filters.Append(f)
}

func (m *Model) AddHaving(f *Filters) {
	m.Havings.Append(f)
}

func (m *Model) AddTable(value string) {
	m.From.Append(&Table{Value: value})
}

func (m *Model) AddGroup(g *Group) {
	m.Groups.Append(g)
}

type Time struct {
	TimeStart          int64
	TimeEnd            int64
	Interval           int
	DatasourceInterval int
	WindowSize         int
}

func (t *Time) AddTimeStart(timeStart int64) {
	if timeStart > t.TimeStart {
		t.TimeStart = timeStart
	}
}

func (t *Time) AddTimeEnd(timeEnd int64) {
	if timeEnd < t.TimeEnd {
		t.TimeEnd = timeEnd
	}
}

func (t *Time) AddInterval(interval int) {
	t.Interval = interval
}

func (t *Time) AddWindowSize(windowSize int) {
	t.WindowSize = windowSize
}

func NewTime() *Time {
	return &Time{
		TimeEnd:            time.Now().Unix(),
		DatasourceInterval: 60,
		WindowSize:         1,
	}
}

type View struct {
	Model         *Model     //初始化view
	SubViewLevels []*SubView //由RawView拆层
}

// 使用model初始化view
func NewView(m *Model) View {
	return View{Model: m}
}

func (v *View) ToString() string {
	buf := bytes.Buffer{}
	v.trans()
	for i, view := range v.SubViewLevels {
		if i > 0 {
			// 将内层view作为外层view的From
			view.From.Append(v.SubViewLevels[i-1])
		}
	}
	//从最外层View开始拼接sql
	v.SubViewLevels[len(v.SubViewLevels)-1].WriteTo(&buf)
	return buf.String()
}

func (v *View) trans() {
	var tagsLevelInner []Node
	var tagsLevelMetrics []Node
	var tagsLevelOuter []Node
	var metricsLevelInner []Node
	var metricsLevelMetrics []Node
	var groupsLevelInner []Node
	var groupsLevelMetrics []Node
	var tagsAliasInner []string
	var groupsValueInner []string
	// 遍历tags，解析至分层结构中
	for _, tag := range v.Model.Tags.tags {
		switch node := tag.(type) {
		case *Tag:
			if node.Flag == NODE_FLAG_METRICS {
				// Tag在最内层中只保留value 去掉alias
				tagsLevelInner = append(tagsLevelInner, tag)
				metricTag := &Tag{}
				if node.Alias != "" {
					metricTag.Value = node.Alias
				} else {
					metricTag.Value = node.Value
				}
				tagsLevelMetrics = append(tagsLevelMetrics, metricTag)
				tagsAliasInner = append(tagsAliasInner, metricTag.Value)
			} else if node.Flag == NODE_FLAG_TRANS {
				// 需要放入最外层的tag
				tagsLevelOuter = append(tagsLevelOuter, tag)
			} else if node.Flag == NODE_FLAG_METRICS_INNER {
				metricsLevelInner = append(metricsLevelInner, tag)
				tagsAliasInner = append(tagsAliasInner, node.Alias)
			} else if node.Flag == NODE_FLAG_METRICS_OUTER {
				metricsLevelMetrics = append(metricsLevelMetrics, tag)
			}
		case Function:
			flag := node.GetFlag()
			node.SetTime(v.Model.Time)
			node.Init()
			if flag == METRICS_FLAG_INNER {
				metricsLevelInner = append(metricsLevelInner, tag)
			} else if flag == METRICS_FLAG_OUTER {
				metricsLevelMetrics = append(metricsLevelMetrics, tag)
			}
		}
	}
	// 计算层拆层的情况下，默认类型的group中with只放在最里层
	for _, node := range v.Model.Groups.groups {
		group := node.(*Group)
		if group.Flag == GROUP_FLAG_DEFAULT {
			groupsLevelInner = append(groupsLevelInner, group)
			groupsLevelMetrics = append(groupsLevelMetrics, &Group{Value: group.Value})
			groupsValueInner = append(groupsValueInner, group.Value)
		} else if group.Flag == GROUP_FLAG_METRICS_OUTER {
			groupsLevelMetrics = append(groupsLevelMetrics, group)
		} else if group.Flag == GROUP_FLAG_METRICS_INNTER {
			groupsLevelInner = append(groupsLevelInner, group)
		}
	}
	if v.Model.MetricsLevelFlag == MODEL_METRICS_LEVEL_FLAG_UNLAY {
		// 计算层不拆层
		// 里层tag+外层metric
		sv := SubView{
			Tags:    &Tags{tags: append(tagsLevelInner, metricsLevelMetrics...)},
			Groups:  v.Model.Groups,
			From:    v.Model.From,
			Filters: v.Model.Filters,
			Havings: v.Model.Havings,
			Orders:  v.Model.Orders,
			Limit:   v.Model.Limit,
		}
		v.SubViewLevels = append(v.SubViewLevels, &sv)
	} else if v.Model.MetricsLevelFlag == MODEL_METRICS_LEVEL_FLAG_LAYERED {
		// 里层的select需要包含所有里层group
		for _, group := range groupsValueInner {
			if !common.IsValueInSliceString(group, tagsAliasInner) {
				tagsLevelInner = append(tagsLevelInner, &Tag{Value: group})
			}
		}
		// 计算层需要拆层
		// 计算层里层
		svInner := SubView{
			Tags:    &Tags{tags: append(tagsLevelInner, metricsLevelInner...)}, // 计算层所有tag及里层算子
			Groups:  &Groups{groups: groupsLevelInner},                         // group分层
			From:    v.Model.From,                                              // 查询表
			Filters: v.Model.Filters,                                           // 所有filter
			Havings: &Filters{},
			Orders:  &Orders{},
			Limit:   &Limit{},
		}
		v.SubViewLevels = append(v.SubViewLevels, &svInner)
		// 计算层外层
		svMetrics := SubView{
			Tags:    &Tags{tags: append(tagsLevelMetrics, metricsLevelMetrics...)}, // 计算层所有tag及外层算子
			Groups:  &Groups{groups: groupsLevelMetrics},                           // group分层
			From:    &Tables{},                                                     // 空table
			Filters: &Filters{},                                                    // 空filter
			Havings: v.Model.Havings,
			Orders:  v.Model.Orders,
			Limit:   v.Model.Limit,
		}
		v.SubViewLevels = append(v.SubViewLevels, &svMetrics)
	}
	if tagsLevelOuter != nil {
		// 翻译层
		svOuter := SubView{
			Tags:    &Tags{tags: tagsLevelOuter}, // 所有翻译层tag
			Groups:  &Groups{},                   // 空group
			From:    &Tables{},                   // 空table
			Filters: &Filters{},                  //空filter
			Havings: &Filters{},
			Orders:  &Orders{},
			Limit:   &Limit{},
		}
		v.SubViewLevels = append(v.SubViewLevels, &svOuter)
	}
}

type SubView struct {
	Tags    *Tags
	Filters *Filters
	From    *Tables
	Groups  *Groups
	Orders  *Orders
	Limit   *Limit
	Havings *Filters
}

func (sv *SubView) GetWiths() []Node {
	var withs []Node
	if nodeWiths := sv.Tags.GetWiths(); nodeWiths != nil {
		withs = append(withs, nodeWiths...)
	}
	if nodeWiths := sv.Filters.GetWiths(); nodeWiths != nil {
		withs = append(withs, nodeWiths...)
	}
	if nodeWiths := sv.Groups.GetWiths(); nodeWiths != nil {
		withs = append(withs, nodeWiths...)
	}
	if nodeWiths := sv.Havings.GetWiths(); nodeWiths != nil {
		withs = append(withs, nodeWiths...)
	}
	return withs
}

func (sv *SubView) ToString() string {
	buf := bytes.Buffer{}
	sv.WriteTo(&buf)
	return buf.String()
}

func (sv *SubView) removeDup(ns NodeSet) []Node {
	// 对NodeSet集合去重
	tmpMap := make(map[string]interface{})
	nodeList := ns.getList()
	targetList := nodeList[:0]
	for _, node := range nodeList {
		str := node.ToString()
		if _, ok := tmpMap[str]; !ok {
			targetList = append(targetList, node)
			tmpMap[str] = nil
		}
	}
	return targetList
}

func (sv *SubView) WriteTo(buf *bytes.Buffer) {
	if nodeWiths := sv.GetWiths(); nodeWiths != nil {
		withs := Withs{Withs: nodeWiths}
		withs.Withs = sv.removeDup(&withs)
		buf.WriteString("WITH ")
		withs.WriteTo(buf)
		buf.WriteString(" ")
	}
	if !sv.Tags.IsNull() {
		sv.Tags.tags = sv.removeDup(sv.Tags)
		buf.WriteString("SELECT ")
		sv.Tags.WriteTo(buf)
	}
	if !sv.From.IsNull() {
		buf.WriteString(" FROM ")
		sv.From.WriteTo(buf)
	}
	if !sv.Filters.IsNull() {
		buf.WriteString(" PREWHERE ")
		sv.Filters.WriteTo(buf)
	}
	if !sv.Groups.IsNull() {
		sv.Groups.groups = sv.removeDup(sv.Groups)
		buf.WriteString(" GROUP BY ")
		sv.Groups.WriteTo(buf)
	}
	if !sv.Havings.IsNull() {
		buf.WriteString(" HAVING ")
		sv.Havings.WriteTo(buf)
	}
	if !sv.Orders.IsNull() {
		buf.WriteString(" ORDER BY ")
		sv.Orders.WriteTo(buf)
	}
	sv.Limit.WriteTo(buf)
}

type Node interface {
	ToString() string
	WriteTo(*bytes.Buffer)
	GetWiths() []Node
}

type NodeSet interface {
	Node
	IsNull() bool
	getList() []Node
}

type NodeBase struct{}

func (n *NodeBase) GetWiths() []Node {
	return nil
}

type NodeSetBase struct{ NodeBase }
