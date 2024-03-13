package common

import (
	"fmt"
	"net"
	"reflect"
	"strings"

	"github.com/deepflowio/deepflow/server/ingester/exporters/config"
	utag "github.com/deepflowio/deepflow/server/ingester/exporters/universal_tag"
	"github.com/deepflowio/deepflow/server/libs/utils"
	logging "github.com/op/go-logging"
)

var log = logging.MustGetLogger("exporters.interface")

type ExportItem interface {
	DataSource() uint32
	GetFieldValueByOffsetAndKind(offset uintptr, kind reflect.Kind, fieldName string) interface{}
	EncodeTo(p config.ExportProtocol, utags *utag.UniversalTagsManager, cfg *config.ExporterCfg) (interface{}, error)
	Release()
	AddReferenceCount()
}

type EncodeItem interface {
	GetFieldValueByOffsetAndKind(offset uintptr, kind reflect.Kind, fieldName string) interface{}
}

var funcMaps = map[string]interface{}{
	"IPv4String": IPv4String,
	"IPv6String": IPv6String,
	"MacString":  MacString,
}

func IPv4String(ip4 uint32) string {
	ip := make(net.IP, 4)
	ip[0] = byte(ip4 >> 24)
	ip[1] = byte(ip4 >> 16)
	ip[2] = byte(ip4 >> 8)
	ip[3] = byte(ip4)
	return ip.String()
}

func IPv6String(ip6 net.IP) string {
	return ip6.String()
}

func MacString(mac uint64) string {
	return utils.Uint64ToMac(mac).String()
}

func GetFunc(funcName string) interface{} {
	return funcMaps[funcName]
}

func writeUnivalsalStr(sb *strings.Builder, universalName string, str string) {
	sb.WriteString(`,"`)
	// delete '_id' of universalName
	if pos := strings.Index(universalName, "_id"); pos != -1 {
		sb.WriteString(universalName[:pos])
		sb.WriteString(universalName[pos+3:]) // 3 is  length of '_id'
	} else {
		sb.WriteString(universalName)
		sb.WriteString("_str")
	}
	sb.WriteString(`":"`)
	sb.WriteString(str)
	sb.WriteString(`"`)
}

func writeTranslatedStr(sb *strings.Builder, name string, str string) {
	sb.WriteString(`,"`)
	sb.WriteString(name)
	sb.WriteString("_str")
	sb.WriteString(`":"`)
	sb.WriteString(str)
	sb.WriteString(`"`)
}

func EncodeToJson(item EncodeItem, dataSourceId int, exporterCfg *config.ExporterCfg, uTags0, uTags1 *utag.UniversalTags) string {
	var sb strings.Builder
	sb.WriteString("{datasource:\"")
	sb.WriteString(config.DataSourceID(dataSourceId).String())
	sb.WriteString(`"`)

	var valueStr string
	var valueFloat64 float64
	for _, structTags := range exporterCfg.ExportFieldStructTags[dataSourceId] {
		isString := false
		isFloat64 := false
		value := item.GetFieldValueByOffsetAndKind(structTags.Offset, structTags.DataType, structTags.FieldName)
		if utils.IsNil(value) {
			log.Debugf("%s is nil", structTags.FieldName)
			continue
		}

		if structTags.ToStringFuncName != "" {
			ret := structTags.ToStringFunc.Call([]reflect.Value{reflect.ValueOf(value)})
			valueStr = ret[0].String()
			isString = true
		} else if structTags.UniversalTagMapID > 0 {
			var universalStr string
			if strings.HasSuffix(structTags.Name, "_1") {
				universalStr = uTags1.GetTagValue(structTags.UniversalTagMapID)
			} else {
				universalStr = uTags0.GetTagValue(structTags.UniversalTagMapID)
			}
			// add origin id
			writeUnivalsalStr(&sb, structTags.Name, universalStr)
		} else if v, ok := value.(string); ok {
			valueStr = v
			isString = true
		} else {
			valueFloat64, isFloat64 = config.ConvertToFloat64(value)
		}

		if structTags.Omitempty && ((isString && valueStr == "") ||
			(isFloat64 && valueFloat64 == 0)) {
			continue
		}

		if structTags.TranslateFile != "" {
			var translatedStr string
			if isString {
				translatedStr = structTags.TranslateStringMap[valueStr]
			} else if isFloat64 {
				translatedStr = structTags.TranslateIntMap[int(valueFloat64)]
			}
			writeTranslatedStr(&sb, structTags.Name, translatedStr)
		}

		sb.WriteString(`,"`)
		sb.WriteString(structTags.Name)
		sb.WriteString(`":`)
		if isString {
			sb.WriteString(`"`)
			sb.WriteString(valueStr)
			sb.WriteString(`"`)
		} else {
			sb.WriteString(fmt.Sprintf("%v", value))
		}
	}
	sb.WriteString("}")
	return sb.String()
}
