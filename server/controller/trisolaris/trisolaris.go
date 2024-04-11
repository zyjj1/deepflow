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

package trisolaris

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/op/go-logging"
	"gorm.io/gorm"

	. "github.com/deepflowio/deepflow/server/controller/common"
	"github.com/deepflowio/deepflow/server/controller/db/mysql"
	"github.com/deepflowio/deepflow/server/controller/election"
	. "github.com/deepflowio/deepflow/server/controller/trisolaris/common"
	"github.com/deepflowio/deepflow/server/controller/trisolaris/config"
	"github.com/deepflowio/deepflow/server/controller/trisolaris/dbmgr"
	"github.com/deepflowio/deepflow/server/controller/trisolaris/kubernetes"
	"github.com/deepflowio/deepflow/server/controller/trisolaris/metadata"
	"github.com/deepflowio/deepflow/server/controller/trisolaris/node"
	"github.com/deepflowio/deepflow/server/controller/trisolaris/refresh"
	"github.com/deepflowio/deepflow/server/controller/trisolaris/utils"
	"github.com/deepflowio/deepflow/server/controller/trisolaris/vtap"
)

var log = logging.MustGetLogger("trisolaris")

type Trisolaris struct {
	config         *config.Config
	metaData       *metadata.MetaData
	vTapInfo       *vtap.VTapInfo
	nodeInfo       *node.NodeInfo
	kubernetesInfo *kubernetes.KubernetesInfo
	startTime      int64
	mDB            *mysql.DB
	ctx            context.Context
	cancel         context.CancelFunc
}

const ORG_ID_INDEX_MAX = ORG_ID_MAX + 1 // 0 index not used

type TrisolarisManager struct {
	orgToTrisolaris [ORG_ID_INDEX_MAX]*Trisolaris
	refreshOP       *refresh.RefreshOP
	teamIDToOrgID   map[string]int
	teamIDStrToInt  map[string]int
	config          *config.Config
	defaultDB       *gorm.DB
	startTime       int64

	// tsdb data
	platformData *atomic.Value // *metadata.PlatformData
	groupProto   *metadata.GroupProto
	policyData   *atomic.Value // *metadata.Policy

	ctx    context.Context
	cancel context.CancelFunc
}

var trisolarisManager *TrisolarisManager

func GetMetaData() *metadata.MetaData {
	return trisolarisManager.orgToTrisolaris[DEFAULT_ORG_ID].metaData
}

func GetOrgInfoByTeamID(teamIDStr string) (orgID int, teamID int) {
	orgID, teamID = trisolarisManager.GetOrgInfoByTeamID(teamIDStr)
	return
}

func GetOrgIDByTeamID(teamID string) int {
	return trisolarisManager.GetOrgIDByTeamID(teamID)
}

func GetGVTapInfo(orgID int) *vtap.VTapInfo {
	return trisolarisManager.GetVTapInfo(orgID)
}

func GetGNodeInfo() *node.NodeInfo {
	return trisolarisManager.orgToTrisolaris[DEFAULT_ORG_ID].nodeInfo
}

// TODO support org
func TeamIDToTrisolaris(teamID string) *Trisolaris {
	return trisolarisManager.orgToTrisolaris[GetOrgIDByTeamID(teamID)]
}

func GetGKubernetesInfo(teamID string) *kubernetes.KubernetesInfo {
	tri := TeamIDToTrisolaris(teamID)
	if tri == nil {
		return nil
	}
	return tri.kubernetesInfo
}

func GetConfig() *config.Config {
	return trisolarisManager.config
}

func GetDB() *gorm.DB {
	return trisolarisManager.defaultDB
}

func GetBillingMethod() string {
	return trisolarisManager.config.BillingMethod
}

func GetGrpcPort() int {
	return trisolarisManager.config.GetGrpcPort()
}

func GetIngesterPort() int {
	return trisolarisManager.config.GetIngesterPort()
}

func GetIsRefused() bool {
	return trisolarisManager.config.GetNoTeamIDRefused()
}

func PutPlatformData() {
	trisolarisManager.orgToTrisolaris[DEFAULT_ORG_ID].metaData.PutChPlatformData()
}

func PutTapType() {
	trisolarisManager.orgToTrisolaris[DEFAULT_ORG_ID].metaData.PutChTapType()
}

func PutNodeInfo() {
	trisolarisManager.orgToTrisolaris[DEFAULT_ORG_ID].nodeInfo.PutChNodeInfo()
}

func PutVTapCache() {
	trisolarisManager.PutVTapCacheRefresh(DEFAULT_ORG_ID)
}

func PutFlowACL() {
	trisolarisManager.orgToTrisolaris[DEFAULT_ORG_ID].metaData.PutChPolicy()
}

func PutGroup() {
	trisolarisManager.orgToTrisolaris[DEFAULT_ORG_ID].metaData.PutChGroup()
}

func getStartTime() int64 {
	startTime := int64(0)
	for {
		startTime = election.GetAcquireTime()
		if startTime == 0 {
			log.Errorf("get start time(%d) failed", startTime)
			time.Sleep(3 * time.Second)
			continue
		}
		break
	}

	log.Infof("get start time(%d) success", startTime)

	return startTime
}

func (t *Trisolaris) Start() {
	log.Infof("start ORG(id=%d database=%s) data generate", t.mDB.ORGID, t.mDB.Name)
	go func() {
		t.metaData.InitData(t.startTime) // 需要先初始化
		go t.metaData.TimedRefreshMetaData()
		go t.kubernetesInfo.TimedRefreshClusterID()
		go t.vTapInfo.Run()
		go t.nodeInfo.TimedRefreshNodeCache()
	}()
}

func (t *Trisolaris) Stop() {
	log.Infof("exit ORG(id=%d database=%s) data generate", t.mDB.ORGID, t.mDB.Name)
	t.cancel()
}

func NewTrisolaris(cfg *config.Config, mDB *mysql.DB, pctx context.Context, startTime int64) *Trisolaris {
	ctx, cancel := context.WithCancel(pctx)
	metaData := metadata.NewMetaData(mDB.DB, cfg, mDB.ORGID, ctx)
	trisolaris := &Trisolaris{
		config:         cfg,
		metaData:       metaData,
		vTapInfo:       vtap.NewVTapInfo(mDB.DB, metaData, cfg, mDB.ORGID, ctx),
		nodeInfo:       node.NewNodeInfo(mDB.DB, metaData, cfg, mDB.ORGID, ctx),
		kubernetesInfo: kubernetes.NewKubernetesInfo(mDB.DB, cfg, mDB.ORGID, ctx),
		startTime:      startTime,
		mDB:            mDB,
		ctx:            ctx,
		cancel:         cancel,
	}

	return trisolaris
}

func NewTrisolarisManager(cfg *config.Config, db *gorm.DB) *TrisolarisManager {
	if trisolarisManager == nil {
		cfg.Convert()
		ctx, cancel := context.WithCancel(context.Background())
		platformData := &atomic.Value{}
		platformData.Store(metadata.NewPlatformData("", "", 0, 0))
		policyData := &atomic.Value{}
		policyData.Store(metadata.NewPolicy(-2, "", 0))
		trisolarisManager = &TrisolarisManager{
			orgToTrisolaris: [ORG_ID_INDEX_MAX]*Trisolaris{},
			refreshOP:       refresh.NewRefreshOP(db, cfg.NodeIP),
			teamIDToOrgID:   make(map[string]int),
			teamIDStrToInt:  make(map[string]int),
			config:          cfg,
			defaultDB:       db,
			platformData:    platformData,
			groupProto:      metadata.NewGroupProto(0, 0),
			policyData:      policyData,
			ctx:             ctx,
			cancel:          cancel,
		}
	}

	return trisolarisManager
}

func (m *TrisolarisManager) Start() error {
	go m.TimedCheckORG()
	go m.refreshOP.TimedRefreshIPs()
	m.startTime = getStartTime()
	m.groupProto.SetStartTime(m.startTime)
	orgIDs, err := mysql.GetORGIDs()
	if err != nil {
		log.Error(err)
		return err
	}
	log.Infof("get orgIDs : %v", orgIDs)
	trisolaris := NewTrisolaris(m.config, mysql.DefaultDB, m.ctx, m.startTime)
	trisolaris.Start()
	m.orgToTrisolaris[DEFAULT_ORG_ID] = trisolaris
	for _, orgID := range orgIDs {
		if m.CheckOrgID(orgID) == false || orgID == DEFAULT_ORG_ID {
			continue
		}
		orgDB, err := mysql.GetDB(orgID)
		if err != nil {
			log.Error(err)
			continue
		}
		trisolaris := NewTrisolaris(m.config, orgDB, m.ctx, m.startTime)
		trisolaris.Start()
		m.orgToTrisolaris[orgID] = trisolaris
	}
	teams, err := dbmgr.DBMgr[mysql.Team](m.defaultDB).Gets()
	if err != nil {
		log.Errorf("get team failed, err(%s)", err)
	}

	teamIDToOrgID := make(map[string]int)
	teamIDStrToInt := make(map[string]int)
	for _, team := range teams {
		teamIDToOrgID[team.ShortLcuuid] = team.OrgLoopID
		teamIDStrToInt[team.ShortLcuuid] = team.LoopID
	}
	m.teamIDToOrgID = teamIDToOrgID
	m.teamIDStrToInt = teamIDStrToInt
	go m.TimedGenerateTSDBData()
	log.Infof("finish orgdata init %v", orgIDs)
	return nil
}

func (m *TrisolarisManager) TeamIDLcuuidToInt(teamID string) int {
	if len(teamID) == 0 {
		return DEFAULT_TEAM_ID
	}
	return m.teamIDStrToInt[teamID]
}

func (m *TrisolarisManager) GetOrgIDByTeamID(teamID string) int {
	if len(teamID) == 0 {
		return DEFAULT_ORG_ID
	}
	return m.teamIDToOrgID[teamID]
}

func (m *TrisolarisManager) GetOrgInfoByTeamID(teamID string) (int, int) {
	if len(teamID) == 0 {
		return DEFAULT_ORG_ID, DEFAULT_TEAM_ID
	}
	return m.teamIDToOrgID[teamID], m.teamIDStrToInt[teamID]
}

func (m *TrisolarisManager) CheckOrgID(orgID int) bool {
	return orgID <= ORG_ID_MAX
}

func (m *TrisolarisManager) PutVTapCacheRefresh(orgID int) {
	if m.CheckOrgID(orgID) == false {
		return
	}
	m.orgToTrisolaris[orgID].vTapInfo.PutVTapCacheRefresh()
}

func (m *TrisolarisManager) GetVTapInfo(orgID int) *vtap.VTapInfo {
	if m.CheckOrgID(orgID) == false {
		log.Errorf("check orgID: %d failed", orgID)
		return nil
	}
	trisolaris := m.orgToTrisolaris[orgID]
	if trisolaris == nil {
		log.Errorf("get orgID: %d failed", orgID)
		return nil
	}
	return trisolaris.vTapInfo
}

func (m *TrisolarisManager) GetVTapCache(orgID int, key string) *vtap.VTapCache {
	vtapInfo := m.GetVTapInfo(orgID)
	if vtapInfo != nil {
		return vtapInfo.GetVTapCache(key)
	}
	return nil
}

func (m *TrisolarisManager) checkORG() {
	orgIDs, err := mysql.GetORGIDs()
	if err != nil {
		log.Error(err)
		return
	}
	for orgID, trisolaris := range m.orgToTrisolaris {
		if orgID == DEFAULT_ORG_ID {
			continue
		}
		if utils.Find[int](orgIDs, orgID) == false {
			if trisolaris != nil {
				m.orgToTrisolaris[orgID] = nil
				trisolaris.Stop()
			}
		} else {
			if trisolaris == nil {
				orgDB, err := mysql.GetDB(orgID)
				if err != nil {
					log.Error(err)
					continue
				}
				trisolaris := NewTrisolaris(m.config, orgDB, m.ctx, m.startTime)
				trisolaris.Start()
				m.orgToTrisolaris[orgID] = trisolaris
			}
		}
	}

	teams, err := dbmgr.DBMgr[mysql.Team](m.defaultDB).Gets()
	if err != nil {
		log.Errorf("get team failed, err(%s)", err)
		return
	}

	teamIDToOrgID := make(map[string]int)
	teamIDStrToInt := make(map[string]int)
	for _, team := range teams {
		teamIDToOrgID[team.ShortLcuuid] = team.OrgLoopID
		teamIDStrToInt[team.ShortLcuuid] = team.LoopID
	}
	m.teamIDToOrgID = teamIDToOrgID
	m.teamIDStrToInt = teamIDStrToInt
}

func (m *TrisolarisManager) TimedCheckORG() {
	interval := time.Duration(60)
	ticker := time.NewTicker(interval * time.Second).C
	for {
		select {
		case <-ticker:
			log.Info("start check org data from timed")
			m.checkORG()
			log.Info("end check org data from timed")
		}
	}
}

func GetIngesterPlatformDataVersion() uint64 {
	return trisolarisManager.getIngesterPlatformDataVersion()
}

func GetIngesterPlatformDataStr() []byte {
	return trisolarisManager.getIngesterPlatformDataStr()
}

func GetIngesterGroupProtoVersion() uint64 {
	return trisolarisManager.getIngesterGroupProtoVersion()
}

func GetIngesterGroupProtoStr() []byte {
	return trisolarisManager.getIngesterGroupProtoStr()
}

func GetIngesterPolicyVersion() uint64 {
	return trisolarisManager.getIngesterPolicyDataVersion()
}

func GetIngesterPolicyStr() []byte {
	return trisolarisManager.getIngesterPolicyDataStr()
}

func (m *TrisolarisManager) getIngesterPlatformData() *metadata.PlatformData {
	return m.platformData.Load().(*metadata.PlatformData)
}

func (m *TrisolarisManager) updateIngesterPlatformData(data *metadata.PlatformData) {
	m.platformData.Store(data)
}

func (m *TrisolarisManager) getIngesterPolicyata() *metadata.Policy {
	return m.policyData.Load().(*metadata.Policy)
}

func (m *TrisolarisManager) updateIngesterPolicyData(data *metadata.Policy) {
	m.policyData.Store(data)
}

func (m *TrisolarisManager) getIngesterPlatformDataVersion() uint64 {
	return m.getIngesterPlatformData().GetPlatformDataVersion()
}

func (m *TrisolarisManager) getIngesterPlatformDataStr() []byte {
	return m.getIngesterPlatformData().GetPlatformDataStr()
}

func (m *TrisolarisManager) getIngesterGroupProtoVersion() uint64 {
	return m.groupProto.GetVersion()
}

func (m *TrisolarisManager) getIngesterGroupProtoStr() []byte {
	return m.groupProto.GetGroups()
}

func (m *TrisolarisManager) getIngesterPolicyDataVersion() uint64 {
	return m.getIngesterPolicyata().GetAllVersion()
}

func (m *TrisolarisManager) getIngesterPolicyDataStr() []byte {
	return m.getIngesterPolicyata().GetAllSerializeString()
}

func (m *TrisolarisManager) generateTSDBData() {
	orgIDs, err := mysql.GetORGIDs()
	if err != nil {
		log.Error(err)
		return
	}

	platformData := metadata.NewPlatformData("platformData", "", 0, PLATFORM_DATA_FOR_INGESTER_MERGE)
	groupData := metadata.NewGroupData(nil, nil)
	policyData := metadata.NewPolicy(-2, "", 0)
	for _, orgID := range orgIDs {
		if m.CheckOrgID(orgID) == false {
			continue
		}
		trisolaris := m.orgToTrisolaris[orgID]
		if trisolaris == nil {
			continue
		}
		orgPlatformData := trisolaris.nodeInfo.GetPlatformData()
		if orgPlatformData != nil {
			platformData.Merge(orgPlatformData)
		}
		orgGroupData := trisolaris.metaData.GetGroupDataOP().GetDropletGroupsData()
		if orgGroupData != nil {
			groupData.Merge(orgGroupData)
		}
		orgPolicyData := trisolaris.metaData.GetPolicyDataOP().GetDropletPolicy()
		policyData.MergeIngesterPolicy(orgPolicyData)

	}
	platformData.GeneratePlatformDataResult()
	m.updateIngesterPlatformData(platformData)
	m.groupProto.GenerateIngesterGroup(groupData)
	policyData.GenerateIngesterData()
	m.updateIngesterPolicyData(policyData)
}

func (m *TrisolarisManager) TimedGenerateTSDBData() {
	m.generateTSDBData()
	interval := time.Duration(60)
	ticker := time.NewTicker(interval * time.Second).C
	for {
		select {
		case <-ticker:
			log.Info("start generate tsdb data from timed")
			m.generateTSDBData()
			log.Info("end generate tsdb data from timed")
		}
	}
}
