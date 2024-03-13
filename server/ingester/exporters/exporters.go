/*
 * Copyright (c) 2024 Yunshan Networks
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package exporters

import (
	"reflect"
	"strings"

	logging "github.com/op/go-logging"

	"github.com/deepflowio/deepflow/server/ingester/exporters/common"
	"github.com/deepflowio/deepflow/server/ingester/exporters/config"
	"github.com/deepflowio/deepflow/server/ingester/exporters/kafka_exporter"
	"github.com/deepflowio/deepflow/server/ingester/exporters/otlp_exporter"
	"github.com/deepflowio/deepflow/server/ingester/exporters/translation"
	"github.com/deepflowio/deepflow/server/ingester/exporters/universal_tag"
	"github.com/deepflowio/deepflow/server/libs/queue"
	"github.com/deepflowio/deepflow/server/libs/utils"
)

var log = logging.MustGetLogger("exporters")

const (
	PUT_BATCH_SIZE               = 1024
	MAX_EXPORTERS_PER_DATASOURCE = 8
)

type Exporter interface {
	// Starts an exporter worker
	Start()
	// Close an exporter worker
	Close()

	// Put sends data to the exporter worker. Worker could decide what to do next. e.g.:
	// - send it out synchronously.
	// - store it in a queue and handle it later.
	Put(items ...interface{})
}

type ExportersCache []interface{}

type Exporters struct {
	config                  *config.Config
	universalTagsManagerMap map[string]*universal_tag.UniversalTagsManager
	translation             *translation.EnumTranslation
	exporters               []Exporter
	dataSourceExporters     [config.MAX_DATASOURCE_ID][]Exporter
	dataSourceExporterCfgs  [config.MAX_DATASOURCE_ID][]*config.ExporterCfg
	putCaches               []ExportersCache // cache for batch put to exporter, has multi decoders call Put(), and put to multi exporters
}

func NewExporters(cfg *config.Config) *Exporters {
	if len(cfg.Exporters) == 0 {
		log.Infof("exporters is empty")
		return nil
	}
	log.Infof("init exporters: %+v", cfg.Exporters)

	putCaches := make([]ExportersCache, config.MAX_DATASOURCE_ID*queue.MAX_QUEUE_COUNT*MAX_EXPORTERS_PER_DATASOURCE)
	translation := translation.NewEnumTranslation()
	exporters := make([]Exporter, 0)
	dataSourceExporters := [config.MAX_DATASOURCE_ID][]Exporter{}
	dataSourceExporterCfgs := [config.MAX_DATASOURCE_ID][]*config.ExporterCfg{}
	var exporter Exporter
	var universalTagManager *universal_tag.UniversalTagsManager
	uTagManagerMap := make(map[string]*universal_tag.UniversalTagsManager)
	for i, exporterCfg := range cfg.Exporters {
		// If the ExportFieldK8s are the same, you can use the same universalTagManager
		uTagKey := strings.Join(exporterCfg.ExportFieldK8s, "-")
		universalTagManager = uTagManagerMap[uTagKey]
		if universalTagManager == nil {
			universalTagManager = universal_tag.NewUniversalTagsManager(exporterCfg.ExportFieldK8s, cfg.Base)
			uTagManagerMap[uTagKey] = universalTagManager
		}
		switch exporterCfg.ExportProtocol {
		case config.PROTOCOL_OTLP:
			exporter = otlp_exporter.NewOtlpExporter(i, &cfg.Exporters[i], universalTagManager)
		case config.PROTOCOL_PROMETHEUS:
			// TO DO
		case config.PROTOCOL_KAFKA:
			exporter = kafka_exporter.NewKafkaExporter(i, &cfg.Exporters[i], universalTagManager)
		default:
			exporter = nil
			log.Warningf("unsupport export protocol %s", exporterCfg.Protocol)
		}
		if exporter == nil {
			continue
		}
		exporters = append(exporters, exporter)
		for _, dataSource := range exporterCfg.DataSources {
			dataSourceId, err := config.ToDataSourceID(dataSource)
			if err != nil {
				log.Warning(err)
				continue
			}
			dataSourceExporters[dataSourceId] = append(dataSourceExporters[dataSourceId], exporter)
			dataSourceExporterCfgs[dataSourceId] = append(dataSourceExporterCfgs[dataSourceId], &cfg.Exporters[i])
		}
	}

	return &Exporters{
		config:                  cfg,
		universalTagsManagerMap: uTagManagerMap,
		exporters:               exporters,
		putCaches:               putCaches,
		dataSourceExporters:     dataSourceExporters,
		dataSourceExporterCfgs:  dataSourceExporterCfgs,
		translation:             translation,
	}
}

func (es *Exporters) Start() {
	for _, v := range es.universalTagsManagerMap {
		v.Start()
	}
	for _, e := range es.exporters {
		e.Start()
	}
}

func (es *Exporters) Close() error {
	for _, v := range es.universalTagsManagerMap {
		v.Close()
	}
	for _, e := range es.exporters {
		e.Close()
	}
	return nil
}

func GetTagFilters(field string, tagFilters []config.TagFilter) []config.TagFilter {
	tagFilter := []config.TagFilter{}
	for _, filter := range tagFilters {
		if filter.FieldName == field {
			tagFilter = append(tagFilter, filter)
		}
	}
	return tagFilter
}

func IsExportField(tag *config.StructTags, exportFieldCategoryBits uint64, exportFieldNames []string) bool {
	if tag.Name == "" {
		return false
	}
	if tag.TagDataSourceBits != 0 && tag.TagDataSourceBits&tag.DataSourceID == 0 {
		return false
	}
	// FIXME check k8s.label
	if tag.CategoryBit&exportFieldCategoryBits != 0 || tag.SubCategoryBit&exportFieldCategoryBits != 0 {
		return true
	}

	for _, name := range exportFieldNames {
		if name == tag.Name {
			return true
		}
	}

	return false
}

func (es *Exporters) initStructTags(item interface{}, dataSourceId uint32, exporterCfg *config.ExporterCfg) {
	if exporterCfg.TagFieltertStructTags[dataSourceId] == nil {
		t := reflect.TypeOf(item)
		if t.Kind() == reflect.Pointer {
			t = t.Elem()
		}
		if t.Kind() != reflect.Struct {
			log.Warningf("item is not struct %v", item)
			return
		}
		num := t.NumField()

		all := make([]config.StructTags, 0, num)
		fields := make([]reflect.StructField, 0, num)
		structFields := []reflect.StructField{}
		for i := 0; i < num; i++ {
			field := t.Field(i)
			dataType := field.Type.Kind()
			if dataType == reflect.Struct {
				structFields = append(structFields, field)
			} else {
				fields = append(fields, field)
			}
		}

		// add all sub struct/interface
		for len(structFields) != 0 {
			sfs := structFields
			structFields = []reflect.StructField{}
			for _, field := range sfs {
				fType := field.Type
				if fType.Kind() != reflect.Struct {
					log.Warningf("ftype is not struct %v", fType)
					continue
				}
				subNum := fType.NumField()
				for i := 0; i < subNum; i++ {
					subField := fType.Field(i)
					dataType := subField.Type.Kind()
					// sub field offset should add parent struct field offset
					subField.Offset += field.Offset
					if dataType == reflect.Struct {
						structFields = append(structFields, subField)
					} else {
						fields = append(fields, subField)
					}
				}
			}
		}

		for _, field := range fields {
			dataType := field.Type.Kind()
			name := field.Tag.Get("json")
			category := field.Tag.Get("category")
			subCategory := field.Tag.Get("sub")

			categoryBit := config.StringToCategoryBit(category)
			subCategoryBit := config.StringToCategoryBit(subCategory)
			omitempty := false
			if field.Tag.Get("omitempty") != "" {
				omitempty = true
			}
			toStringFuncName := field.Tag.Get("to_string")
			toStringFunc := reflect.ValueOf(common.GetFunc(toStringFuncName))

			dataSourceStr := field.Tag.Get("datasource")
			dataSourceBits := config.TagStringToDataSourceBits(dataSourceStr)

			translateFile := field.Tag.Get("translate")
			structTag := config.StructTags{
				DataSourceID:      dataSourceId,
				Name:              name,
				FieldName:         field.Name,
				Category:          category + "." + subCategory,
				CategoryBit:       categoryBit,
				SubCategoryBit:    subCategoryBit,
				Offset:            field.Offset,
				DataType:          dataType,
				Omitempty:         omitempty,
				TranslateFile:     translateFile,
				ToStringFuncName:  toStringFuncName,
				ToStringFunc:      toStringFunc,
				UniversalTagMapID: universal_tag.StringToUniversalTagID(name),
				TagFilters:        GetTagFilters(name, exporterCfg.TagFilters),
				TagDataSourceBits: dataSourceBits,
			}
			if translateFile != "" {
				structTag.TranslateIntMap, structTag.TranslateStringMap = es.translation.GetMaps(translateFile)
			}
			structTag.IsExportedField = IsExportField(&structTag, exporterCfg.ExportFieldCategoryBits, exporterCfg.ExportFieldNames)
			all = append(all, structTag)
		}

		tagFieltertStructTags := []config.StructTags{}
		exportFieldStructTags := []config.StructTags{}
		for _, structTag := range all {
			if len(structTag.TagFilters) > 0 {
				tagFieltertStructTags = append(tagFieltertStructTags, structTag)
			}
			if structTag.IsExportedField {
				exportFieldStructTags = append(exportFieldStructTags, structTag)
			}
		}
		exporterCfg.TagFieltertStructTags[dataSourceId] = tagFieltertStructTags
		exporterCfg.ExportFieldStructTags[dataSourceId] = exportFieldStructTags

		dsid := config.DataSourceID(dataSourceId)
		log.Infof("datasource %s, get all structTags: %+v", dsid.String(), all)
		log.Infof("datasource %s, get tagfilter structTags: %+v", dsid.String(), tagFieltertStructTags)
		log.Infof("datasource %s, get exportfield structTags: %+v", dsid.String(), exportFieldStructTags)
	}
}

func (es *Exporters) IsExportItem(item common.ExportItem, dataSourceId uint32, exporterCfg *config.ExporterCfg) bool {
	es.initStructTags(item, dataSourceId, exporterCfg)
	for _, structTag := range exporterCfg.TagFieltertStructTags[dataSourceId] {
		value := item.GetFieldValueByOffsetAndKind(structTag.Offset, structTag.DataType, structTag.FieldName)
		for _, tagFilter := range structTag.TagFilters {
			if !tagFilter.MatchValue(value) {
				// todo add counter
				return false
			}
		}
	}

	return true
}

func (es *Exporters) getPutCache(dataSourceId, decoderId, exporterId int) *ExportersCache {
	return &es.putCaches[(dataSourceId*queue.MAX_QUEUE_COUNT+decoderId)*MAX_EXPORTERS_PER_DATASOURCE+exporterId]
}

// parallel put
func (es *Exporters) Put(decoderIndex int, item common.ExportItem) {
	dataSourceId := item.DataSource()
	if es.dataSourceExporters[dataSourceId] == nil {
		return
	}
	if utils.IsNil(item) {
		es.Flush(int(dataSourceId), decoderIndex)
		return
	}
	exporters := es.dataSourceExporters[dataSourceId]
	if len(exporters) == 0 {
		return
	}
	exporterCfgs := es.dataSourceExporterCfgs[dataSourceId]
	for i, e := range exporters {
		if !es.IsExportItem(item, dataSourceId, exporterCfgs[i]) {
			continue
		}
		exportersCache := es.getPutCache(int(dataSourceId), decoderIndex, i)
		item.AddReferenceCount()
		*exportersCache = append(*exportersCache, item)
		if len(*exportersCache) >= PUT_BATCH_SIZE {
			e.Put(*exportersCache...)
			*exportersCache = (*exportersCache)[:0]
		}
	}
}

func (es *Exporters) Flush(dataSourceId, decoderIndex int) {
	exporters := es.dataSourceExporters[dataSourceId]
	if len(exporters) == 0 {
		return
	}
	for i, e := range exporters {
		exportersCache := es.getPutCache(int(dataSourceId), decoderIndex, i)
		if len(*exportersCache) >= 0 {
			e.Put(*exportersCache...)
			*exportersCache = (*exportersCache)[:0]
		}
	}
}
