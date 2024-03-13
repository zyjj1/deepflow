package config

import (
	"fmt"
	"io/ioutil"
	"os"
	"reflect"
	"strings"

	logging "github.com/op/go-logging"
	yaml "gopkg.in/yaml.v2"

	"github.com/deepflowio/deepflow/server/ingester/config"
	flow_metrics "github.com/deepflowio/deepflow/server/libs/flow-metrics"
)

var log = logging.MustGetLogger("exporters_config")

const (
	DefaultExportQueueCount = 4
	DefaultExportQueueSize  = 100000
	DefaultExportBatchSize  = 32
)

type DataSourceID uint32

const (
	NETWORK_1M         = DataSourceID(flow_metrics.NETWORK_1M)
	NETWORK_MAP_1M     = DataSourceID(flow_metrics.NETWORK_MAP_1M)
	APPLICATION_1M     = DataSourceID(flow_metrics.APPLICATION_1M)
	APPLICATION_MAP_1M = DataSourceID(flow_metrics.APPLICATION_MAP_1M)

	NETWORK_1S         = DataSourceID(flow_metrics.NETWORK_1S)
	NETWORK_MAP_1S     = DataSourceID(flow_metrics.NETWORK_MAP_1S)
	APPLICATION_1S     = DataSourceID(flow_metrics.APPLICATION_1S)
	APPLICATION_MAP_1S = DataSourceID(flow_metrics.APPLICATION_MAP_1S)

	PERF_EVENT = DataSourceID(flow_metrics.METRICS_TABLE_ID_MAX) + 1 + iota

	L4_FLOW_LOG
	L7_FLOW_LOG

	MAX_DATASOURCE_ID
)

var dataSourceStrings = []string{
	NETWORK_1M:         "flow_metrics.network.1m",
	NETWORK_MAP_1M:     "flow_metrics.network_map.1m",
	APPLICATION_1M:     "flow_metrics.application.1m",
	APPLICATION_MAP_1M: "flow_metrics.application_map.1m",
	NETWORK_1S:         "flow_metrics.network.1s",
	NETWORK_MAP_1S:     "flow_metrics.network_map.1s",
	APPLICATION_1S:     "flow_metrics.application.1s",
	APPLICATION_MAP_1S: "flow_metrics.application_map.1s",
	PERF_EVENT:         "event.perf_event",
	L4_FLOW_LOG:        "flow_log.l4_flow_log",
	L7_FLOW_LOG:        "flow_log.l7_flow_log",
	MAX_DATASOURCE_ID:  "invalid_datasource",
}

func ToDataSourceID(str string) (DataSourceID, error) {
	for i, v := range dataSourceStrings {
		if v == str {
			return DataSourceID(i), nil
		}
	}
	return MAX_DATASOURCE_ID, fmt.Errorf("invalid datasource %s", str)
}

func StringsToDataSourceBits(strs []string) uint32 {
	ret := uint32(0)
	for _, str := range strs {
		t, err := ToDataSourceID(str)
		if err != nil {
			log.Warningf("unknown export datasource: %s", str)
			continue
		}
		ret |= (1 << uint32(t))
	}
	return ret
}

func (d DataSourceID) String() string {
	return dataSourceStrings[d]
}

// n|nm|a|am
func TagStringToDataSourceBits(s string) uint32 {
	ret := uint32(0)
	if s == "" {
		return 0
	}
	dss := strings.Split(s, "|")
	for _, ds := range dss {
		switch ds {
		case "n":
			ret |= 1 << uint32(NETWORK_1M)
			ret |= 1 << uint32(NETWORK_1S)
		case "nm":
			ret |= 1 << uint32(NETWORK_MAP_1M)
			ret |= 1 << uint32(NETWORK_MAP_1S)
		case "a":
			ret |= 1 << uint32(APPLICATION_1M)
			ret |= 1 << uint32(APPLICATION_1S)
		case "am":
			ret |= 1 << uint32(APPLICATION_MAP_1M)
			ret |= 1 << uint32(APPLICATION_MAP_1S)
		}
	}
	return ret
}

type Operator uint8

const (
	EQ Operator = iota
	NEQ
	IN
	NOT_IN
	WILDCARD_EQ
	WILDCARD_NEQ
	REGEXP_EQ
	REGEXP_NEQ

	MAX_OPERATOR_ID
)

var operatorStrings = [MAX_OPERATOR_ID]string{
	EQ:           "=",
	NEQ:          "!=",
	IN:           "in",
	NOT_IN:       "not in",
	WILDCARD_EQ:  ":",
	WILDCARD_NEQ: "!:",
	REGEXP_EQ:    "~",
	REGEXP_NEQ:   "!~",
}

func (o Operator) String() string {
	return operatorStrings[o]
}

type TagFilter struct {
	FieldName   string   `yaml:"field-name"`
	Operator    string   `yaml:"operator"`
	FieldValues []string `yaml:"field-values"`

	OperatorID   Operator
	ValueStrings []string
	ValueFloat64 []float64
}

func (t *TagFilter) MatchStringValue(value string) bool {
	switch t.OperatorID {
	case EQ:
		for _, v := range t.ValueStrings {
			if v != value {
				return false
			}
		}
	case NEQ:
		for _, v := range t.ValueStrings {
			if v == value {
				return false
			}
		}
	case IN:
		for _, v := range t.ValueStrings {
			if v != value {
				return false
			}
		}
	case NOT_IN:
		for _, v := range t.ValueStrings {
			if v == value {
				return false
			}
		}
	default:
		return true
	}
	return true
}

func (t *TagFilter) MatchFloatValue(value float64) bool {
	switch t.OperatorID {
	case EQ:
		for _, v := range t.ValueFloat64 {
			if v != value {
				return false
			}
		}
	case NEQ:
		for _, v := range t.ValueFloat64 {
			if v == value {
				return false
			}
		}
	case IN:
		for _, v := range t.ValueFloat64 {
			if v != value {
				return false
			}
		}
	case NOT_IN:
		for _, v := range t.ValueFloat64 {
			if v == value {
				return false
			}
		}
	default:
		return true
	}
	return true
}

func (t *TagFilter) MatchValue(value interface{}) bool {
	var float64Value float64
	var isFloat64 bool
	strValue, isStr := value.(string)
	if !isStr {
		float64Value, isFloat64 = ConvertToFloat64(value)
	}

	if !isStr && !isFloat64 {
		return true
	}

	if isStr {
		return t.MatchStringValue(strValue)
	} else if isFloat64 {
		return t.MatchFloatValue(float64Value)
	}
	return true
}

type TranslateMapID uint8

const (
	TRANSLATION_NONE uint8 = iota
	AUTO_INSTANCE_TYPE
	AUTO_SERVICE_TYPE
	CAPTURE_NIC_TYPE
	CLOSE_TYPE
	ETH_TYPE
	EVENT_LEVEL
	EVENT_SIGNAL_SOURCE
	EVENT_TYPE
	INSTANCE_TYPE
	IP_TYPE

	L7_PROTOCOL
)

var translateMapStrings = []string{
	TRANSLATION_NONE:   "translation_none",
	AUTO_INSTANCE_TYPE: "auto_instance_type",
	AUTO_SERVICE_TYPE:  "auto_service_type",
	CAPTURE_NIC_TYPE:   "capture_nic_type",
	L7_PROTOCOL:        "l7_protocol",
}

type StructTags struct {
	DataSourceID       uint32
	Name               string // tag: 'json'
	FieldName          string // field name
	Offset             uintptr
	Category           string // tag: 'category'
	CategoryBit        uint64 // gen from tag: 'category'
	SubCategoryBit     uint64 // gen from tag: 'sub'
	ToStringFuncName   string // tag: 'to_string'
	ToStringFunc       reflect.Value
	DataType           reflect.Kind
	TranslateFile      string            // tag: 'translate': as l7_protocol, from server/querier/db_descriptions/clickhouse/tag/enum/*
	TranslateIntMap    map[int]string    // gen from content of `TranslateFile`
	TranslateStringMap map[string]string // gen from content of `TranslateFile`
	UniversalTagMapID  uint8             // gen from universal tags, region_id,az_id ...
	Omitempty          bool              // tag: 'omitempty'
	TagDataSourceStr   string            // tag: 'datasource'
	TagDataSourceBits  uint32            // gen from 'DatasourceStr'

	// the field has tagFilter, if TagFilter not nil, should caculate filter
	TagFilters []TagFilter

	// gen by `ExportFields`
	IsExportedField bool
}

// ExporterCfg holds configs of different exporters.
type ExporterCfg struct {
	Protocol       string         `yaml:"protocol"`
	ExportProtocol ExportProtocol // gen by `Protocol`
	DataSources    []string       `yaml:"data-sources"`
	DataSourceBits uint32         // gen by `DataSources`
	Endpoints      []string       `yaml:"endpoints"`
	QueueCount     int            `yaml:"queue-count"`
	QueueSize      int            `yaml:"queue-size"`
	BatchSize      int            `yaml:"batch-size"`
	FlusTimeout    int            `yaml:"flush-timeout"`

	TagFilters              []TagFilter `yaml:"tag-filters"`
	ExportFields            []string    `yaml:"export-fields"`
	ExportFieldCategoryBits uint64      // gen by `ExportFields`
	ExportFieldNames        []string    // gen by `ExportFields`
	ExportFieldK8s          []string    // gen by `ExportFields`

	ExportFieldStructTags [MAX_DATASOURCE_ID][]StructTags // gen by `ExportFields` and init when exporting item first time
	TagFieltertStructTags [MAX_DATASOURCE_ID][]StructTags // gen by `TagFilters`  and init when exporting item first time

	// private configuration
	ExtraHeaders map[string]string `yaml:"extra-headers"`

	// for Otlp l7_flow_log exporter

	// for promemtheus

	// for kafka
}

type ExportProtocol uint8

const (
	PROTOCOL_OTLP ExportProtocol = iota
	PROTOCOL_PROMETHEUS
	PROTOCOL_KAFKA

	MAX_PROTOCOL_ID
)

var protocolToStrings = []string{
	PROTOCOL_OTLP:       "opentelemetry",
	PROTOCOL_PROMETHEUS: "promethes",
	PROTOCOL_KAFKA:      "kafka",
	MAX_PROTOCOL_ID:     "unknown",
}

func stringToExportProtocol(str string) ExportProtocol {
	for i, v := range protocolToStrings {
		if v == str {
			return ExportProtocol(i)
		}
	}
	log.Warningf("unsupport export protocol: %s", str)
	return MAX_PROTOCOL_ID
}

func (p ExportProtocol) String() string {
	return protocolToStrings[p]
}

func (cfg *ExporterCfg) Validate() error {
	if cfg.BatchSize == 0 {
		cfg.BatchSize = DefaultExportBatchSize
	}

	if cfg.QueueCount == 0 {
		cfg.QueueCount = DefaultExportQueueCount
	}
	if cfg.QueueSize == 0 {
		cfg.QueueSize = DefaultExportQueueSize
	}
	cfg.DataSourceBits = StringsToDataSourceBits(cfg.DataSources)
	cfg.ExportFieldCategoryBits = StringsToCategoryBits(cfg.ExportFields)
	cfg.ExportFieldNames = cfg.ExportFields
	cfg.ExportProtocol = stringToExportProtocol(cfg.Protocol)
	cfg.ExportFieldK8s = GetK8sLabelConfigs(cfg.ExportFields)
	return nil
}

type ExportersConfig struct {
	Exporters Config `yaml:"ingester"`
}

type Config struct {
	Base      *config.Config
	Exporters []ExporterCfg `yaml:"exporters"`
}

func (c *Config) Validate() error {
	for i := range c.Exporters {
		if err := c.Exporters[i].Validate(); err != nil {
			return err
		}
	}
	return nil
}

var DefaultOtlpExportCategory = []string{"service_info", "tracing_info", "network_layer", "flow_info", "transport_layer", "application_layer", "metrics"}

func bitsToString(bits uint64, strMap map[string]uint64) string {
	ret := ""
	for k, v := range strMap {
		if bits&v != 0 {
			if len(ret) == 0 {
				ret = k
			} else {
				ret = ret + "," + k
			}
		}
	}
	return ret
}

const (
	UNKNOWN_CATEGORY = 0

	TAG uint64 = 1 << iota
	FLOW_INFO
	UNIVERSAL_TAG
	CUSTOM_TAG
	NATIVE_TAG
	NETWORK_LAYER
	TUNNEL_INFO
	TRANSPORT_LAYER
	APPLICATION_LAYER
	SERVICE_INFO
	TRACING_INFO
	CAPTURE_INFO
	EVENT_INFO // perf_event only
	K8S_LABEL

	METRICS
	L3_THROUGHPUT // network*/l4_flow_log
	L4_THROUGHPUT // network*/l4_flow_log
	TCP_SLOW      // network*/l4_flow_log
	TCP_ERROR     // network*/l4_flow_log
	APPLICATION   // network*/l4_flow_log
	THROUGHPUT    // application*/l7_flow_log
	ERROR         // application*/l7_flow_log
	DELAY         // all
)

var categoryStringMap = map[string]uint64{
	"tag":               TAG,
	"flow_info":         FLOW_INFO,
	"universal_tag":     UNIVERSAL_TAG,
	"custom_tag":        CUSTOM_TAG,
	"native_tag":        NATIVE_TAG,
	"network_layer":     NETWORK_LAYER,
	"tunnel_info":       TUNNEL_INFO,
	"transport_layer":   TRANSPORT_LAYER,
	"application_layer": APPLICATION_LAYER,
	"service_info":      SERVICE_INFO,
	"tracing_info":      TRACING_INFO,
	"capture_info":      CAPTURE_INFO,
	"k8s_label":         K8S_LABEL,

	"metrics":       METRICS,
	"l3_throughput": L3_THROUGHPUT,
	"l4_throughput": L4_THROUGHPUT,
	"tcp_slow":      TCP_SLOW,
	"tcp_error":     TCP_ERROR,
	"application":   APPLICATION,
	"throughput":    THROUGHPUT,
	"error":         ERROR,
	"delay":         DELAY,
}

func StringToCategoryBit(str string) uint64 {
	if str == "" {
		return UNKNOWN_CATEGORY
	}
	t, ok := categoryStringMap[str]
	if !ok {
		log.Warningf("unknown export category: %s", str)
	}
	return uint64(t)
}

func StringsToCategoryBits(strs []string) uint64 {
	ret := uint64(0)
	for _, str := range strs {
		if !strings.HasPrefix(str, "@") {
			continue
		}
		// format: 'category.subcategory'
		categorys := strings.Split(str[1:], ".")
		category := categorys[0]
		if len(categorys) > 1 {
			category = categorys[1]
		}
		t, ok := categoryStringMap[category]
		if !ok {
			log.Warningf("unknown export category: %s", str)
			continue
		}
		ret |= t
	}
	return ret
}

func GetK8sLabelConfigs(strs []string) []string {
	ret := []string{}
	for _, str := range strs {
		if strings.HasPrefix(str, "k8s") || strings.HasPrefix(str, "~k8s") {
			ret = append(ret, str)
		}
	}
	return ret
}

func CategoryBitsToString(bits uint64) string {
	return bitsToString(bits, categoryStringMap)
}

func Load(base *config.Config, path string) *Config {
	config := &ExportersConfig{
		Exporters: Config{
			Base: base,
		},
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		log.Info("no config file, use defaults")
		return &config.Exporters
	}
	configBytes, err := ioutil.ReadFile(path)
	if err != nil {
		log.Warning("Read config file error:", err)
		config.Exporters.Validate()
		return &config.Exporters
	}
	if err = yaml.Unmarshal(configBytes, &config); err != nil {
		log.Error("Unmarshal yaml error:", err)
		os.Exit(1)
	}

	if err = config.Exporters.Validate(); err != nil {
		log.Error(err)
		os.Exit(1)
	}
	return &config.Exporters
}

func ConvertToFloat64(data interface{}) (float64, bool) {
	switch v := data.(type) {
	case uint:
		return float64(v), true
	case uint8:
		return float64(v), true
	case uint16:
		return float64(v), true
	case uint32:
		return float64(v), true
	case uint64:
		return float64(v), true
	case uintptr:
		return float64(v), true
	case int:
		return float64(v), true
	case int8:
		return float64(v), true
	case int16:
		return float64(v), true
	case int32:
		return float64(v), true
	case int64:
		return float64(v), true
	case float64:
		return v, true
	default:
		return 0, false
	}
}
