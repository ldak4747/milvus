// Licensed to the LF AI & Data foundation under one
// or more contributor license agreements. See the NOTICE file
// distributed with this work for additional information
// regarding copyright ownership. The ASF licenses this file
// to you under the Apache License, Version 2.0 (the
// "License"); you may not use this file except in compliance
// with the License. You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package meta

import (
	"errors"
	"sync"

	"github.com/milvus-io/milvus/internal/log"
	"github.com/milvus-io/milvus/internal/proto/querypb"
	"github.com/milvus-io/milvus/internal/querycoordv2/session"
	"github.com/milvus-io/milvus/internal/util/typeutil"
	. "github.com/milvus-io/milvus/internal/util/typeutil"
	"github.com/samber/lo"
	"go.uber.org/zap"
)

var (
	ErrNodeAlreadyAssign            = errors.New("node already assign to other resource group")
	ErrRGIsFull                     = errors.New("resource group is full")
	ErrRGIsEmpty                    = errors.New("resource group is empty")
	ErrRGNotExist                   = errors.New("resource group doesn't exist")
	ErrRGAlreadyExist               = errors.New("resource group already exist")
	ErrRGAssignNodeFailed           = errors.New("failed to assign node to resource group")
	ErrRGUnAssignNodeFailed         = errors.New("failed to unassign node from resource group")
	ErrSaveResourceGroupToStore     = errors.New("failed to save resource group to store")
	ErrRemoveResourceGroupFromStore = errors.New("failed to remove resource group from store")
	ErrRecoverResourceGroupToStore  = errors.New("failed to recover resource group to store")
	ErrNodeNotAssignToRG            = errors.New("node hasn't been assign to any resource group")
	ErrRGNameIsEmpty                = errors.New("resource group name couldn't be empty")
	ErrDeleteDefaultRG              = errors.New("delete default rg is not permitted")
	ErrDeleteNonEmptyRG             = errors.New("delete non-empty rg is not permitted")
	ErrNodeNotExist                 = errors.New("node does not exist")
	ErrNodeStopped                  = errors.New("node has been stopped")
	ErrRGLimit                      = errors.New("resource group num reach limit 1024")
	ErrNodeNotEnough                = errors.New("nodes not enough")
)

var DefaultResourceGroupName = "__default_resource_group"

type ResourceGroup struct {
	nodes    UniqueSet
	capacity int
}

func NewResourceGroup(capacity int) *ResourceGroup {
	rg := &ResourceGroup{
		nodes:    typeutil.NewUniqueSet(),
		capacity: capacity,
	}

	return rg
}

// assign node to resource group
func (rg *ResourceGroup) assignNode(id int64) error {
	if rg.containsNode(id) {
		return ErrNodeAlreadyAssign
	}

	rg.nodes.Insert(id)
	rg.capacity++

	return nil
}

// unassign node from resource group
func (rg *ResourceGroup) unassignNode(id int64) error {
	if !rg.containsNode(id) {
		// remove non exist node should be tolerable
		return nil
	}

	rg.nodes.Remove(id)
	rg.capacity--

	return nil
}

func (rg *ResourceGroup) handleNodeUp(id int64) error {
	if rg.LackOfNodes() == 0 {
		return ErrRGIsFull
	}

	if rg.containsNode(id) {
		return ErrNodeAlreadyAssign
	}

	rg.nodes.Insert(id)
	return nil
}

func (rg *ResourceGroup) handleNodeDown(id int64) error {
	if !rg.containsNode(id) {
		// remove non exist node should be tolerable
		return nil
	}

	rg.nodes.Remove(id)
	return nil
}

func (rg *ResourceGroup) LackOfNodes() int {
	return rg.capacity - len(rg.nodes)
}

func (rg *ResourceGroup) containsNode(id int64) bool {
	return rg.nodes.Contain(id)
}

func (rg *ResourceGroup) GetNodes() []int64 {
	return rg.nodes.Collect()
}

func (rg *ResourceGroup) GetCapacity() int {
	return rg.capacity
}

type ResourceManager struct {
	groups  map[string]*ResourceGroup
	store   Store
	nodeMgr *session.NodeManager

	rwmutex sync.RWMutex
}

func NewResourceManager(store Store, nodeMgr *session.NodeManager) *ResourceManager {
	groupMap := make(map[string]*ResourceGroup)
	groupMap[DefaultResourceGroupName] = NewResourceGroup(1000000)
	return &ResourceManager{
		groups:  groupMap,
		store:   store,
		nodeMgr: nodeMgr,
	}
}

func (rm *ResourceManager) AddResourceGroup(rgName string) error {
	rm.rwmutex.Lock()
	defer rm.rwmutex.Unlock()
	if len(rgName) == 0 {
		return ErrRGNameIsEmpty
	}

	if rm.groups[rgName] != nil {
		return ErrRGAlreadyExist
	}

	if len(rm.groups) >= 1024 {
		return ErrRGLimit
	}

	err := rm.store.SaveResourceGroup(&querypb.ResourceGroup{
		Name:     rgName,
		Capacity: 0,
	})
	if err != nil {
		log.Info("failed to add resource group",
			zap.String("rgName", rgName),
			zap.Error(err),
		)
		return err
	}
	rm.groups[rgName] = NewResourceGroup(0)

	log.Info("add resource group",
		zap.String("rgName", rgName),
	)
	return nil
}

func (rm *ResourceManager) RemoveResourceGroup(rgName string) error {
	rm.rwmutex.Lock()
	defer rm.rwmutex.Unlock()
	if rgName == DefaultResourceGroupName {
		return ErrDeleteDefaultRG
	}

	if rm.groups[rgName] == nil {
		// delete a non-exist rg should be tolerable
		return nil
	}

	if rm.groups[rgName].GetCapacity() != 0 {
		return ErrDeleteNonEmptyRG
	}

	err := rm.store.RemoveResourceGroup(rgName)
	if err != nil {
		log.Info("failed to remove resource group",
			zap.String("rgName", rgName),
			zap.Error(err),
		)
		return err
	}
	delete(rm.groups, rgName)

	log.Info("remove resource group",
		zap.String("rgName", rgName),
	)
	return nil
}

func (rm *ResourceManager) AssignNode(rgName string, node int64) error {
	rm.rwmutex.Lock()
	defer rm.rwmutex.Unlock()
	return rm.assignNode(rgName, node)
}

func (rm *ResourceManager) assignNode(rgName string, node int64) error {
	if rm.groups[rgName] == nil {
		return ErrRGNotExist
	}

	if rm.nodeMgr.Get(node) == nil {
		return ErrNodeNotExist
	}

	if ok, _ := rm.nodeMgr.IsStoppingNode(node); ok {
		return ErrNodeStopped
	}

	rm.checkRGNodeStatus(rgName)
	if rm.checkNodeAssigned(node) {
		return ErrNodeAlreadyAssign
	}

	newNodes := rm.groups[rgName].GetNodes()
	newNodes = append(newNodes, node)
	err := rm.store.SaveResourceGroup(&querypb.ResourceGroup{
		Name:     rgName,
		Capacity: int32(rm.groups[rgName].GetCapacity()) + 1,
		Nodes:    newNodes,
	})
	if err != nil {
		log.Info("failed to add node to resource group",
			zap.String("rgName", rgName),
			zap.Int64("node", node),
			zap.Error(err),
		)
		return err
	}

	err = rm.groups[rgName].assignNode(node)
	if err != nil {
		return err
	}

	log.Info("add node to resource group",
		zap.String("rgName", rgName),
		zap.Int64("node", node),
	)

	return nil
}

func (rm *ResourceManager) checkNodeAssigned(node int64) bool {
	for _, group := range rm.groups {
		if group.containsNode(node) {
			return true
		}
	}

	return false
}

func (rm *ResourceManager) UnassignNode(rgName string, node int64) error {
	rm.rwmutex.Lock()
	defer rm.rwmutex.Unlock()

	return rm.unassignNode(rgName, node)
}

func (rm *ResourceManager) unassignNode(rgName string, node int64) error {
	if rm.groups[rgName] == nil {
		return ErrRGNotExist
	}

	if rm.nodeMgr.Get(node) == nil {
		// remove non exist node should be tolerable
		return nil
	}

	newNodes := make([]int64, 0)
	for nid := range rm.groups[rgName].nodes {
		if nid != node {
			newNodes = append(newNodes, nid)
		}
	}

	err := rm.store.SaveResourceGroup(&querypb.ResourceGroup{
		Name:     rgName,
		Capacity: int32(rm.groups[rgName].GetCapacity()) - 1,
		Nodes:    newNodes,
	})
	if err != nil {
		log.Info("remove node from resource group",
			zap.String("rgName", rgName),
			zap.Int64("node", node),
			zap.Error(err),
		)
		return err
	}

	rm.checkRGNodeStatus(rgName)
	err = rm.groups[rgName].unassignNode(node)
	if err != nil {
		return err
	}

	log.Info("remove node from resource group",
		zap.String("rgName", rgName),
		zap.Int64("node", node),
	)

	return nil
}

func (rm *ResourceManager) GetNodes(rgName string) ([]int64, error) {
	rm.rwmutex.RLock()
	defer rm.rwmutex.RUnlock()
	if rm.groups[rgName] == nil {
		return nil, ErrRGNotExist
	}

	rm.checkRGNodeStatus(rgName)

	return rm.groups[rgName].GetNodes(), nil
}

// return all outbound node
func (rm *ResourceManager) CheckOutboundNodes(replica *Replica) typeutil.UniqueSet {
	rm.rwmutex.RLock()
	defer rm.rwmutex.RUnlock()

	if rm.groups[replica.GetResourceGroup()] == nil {
		return typeutil.NewUniqueSet()
	}
	rg := rm.groups[replica.GetResourceGroup()]

	ret := typeutil.NewUniqueSet()
	for _, node := range replica.GetNodes() {
		if !rg.containsNode(node) {
			ret.Insert(node)
		}
	}

	return ret
}

// return outgoing node num on each rg from this replica
func (rm *ResourceManager) GetOutgoingNodeNumByReplica(replica *Replica) map[string]int32 {
	rm.rwmutex.RLock()
	defer rm.rwmutex.RUnlock()

	if rm.groups[replica.GetResourceGroup()] == nil {
		return nil
	}

	rg := rm.groups[replica.GetResourceGroup()]
	ret := make(map[string]int32)
	for _, node := range replica.GetNodes() {
		if !rg.containsNode(node) {
			rgName, err := rm.findResourceGroupByNode(node)
			if err == nil {
				ret[rgName]++
			}
		}
	}

	return ret
}

func (rm *ResourceManager) ContainsNode(rgName string, node int64) bool {
	rm.rwmutex.RLock()
	defer rm.rwmutex.RUnlock()
	if rm.groups[rgName] == nil {
		return false
	}

	rm.checkRGNodeStatus(rgName)
	return rm.groups[rgName].containsNode(node)
}

func (rm *ResourceManager) ContainResourceGroup(rgName string) bool {
	rm.rwmutex.RLock()
	defer rm.rwmutex.RUnlock()
	return rm.groups[rgName] != nil
}

func (rm *ResourceManager) GetResourceGroup(rgName string) (*ResourceGroup, error) {
	rm.rwmutex.RLock()
	defer rm.rwmutex.RUnlock()

	if rm.groups[rgName] == nil {
		return nil, ErrRGNotExist
	}

	rm.checkRGNodeStatus(rgName)
	return rm.groups[rgName], nil
}

func (rm *ResourceManager) ListResourceGroups() []string {
	rm.rwmutex.RLock()
	defer rm.rwmutex.RUnlock()

	return lo.Keys(rm.groups)
}

func (rm *ResourceManager) FindResourceGroupByNode(node int64) (string, error) {
	rm.rwmutex.RLock()
	defer rm.rwmutex.RUnlock()

	return rm.findResourceGroupByNode(node)
}

func (rm *ResourceManager) findResourceGroupByNode(node int64) (string, error) {
	for name, group := range rm.groups {
		if group.containsNode(node) {
			return name, nil
		}
	}

	return "", ErrNodeNotAssignToRG
}

func (rm *ResourceManager) HandleNodeUp(node int64) (string, error) {
	rm.rwmutex.Lock()
	defer rm.rwmutex.Unlock()

	if rm.nodeMgr.Get(node) == nil {
		return "", ErrNodeNotExist
	}

	if ok, _ := rm.nodeMgr.IsStoppingNode(node); ok {
		return "", ErrNodeStopped
	}

	// if node already assign to rg
	rgName, err := rm.findResourceGroupByNode(node)
	if err == nil {
		log.Info("HandleNodeUp: node already assign to resource group",
			zap.String("rgName", rgName),
			zap.Int64("node", node),
		)
		return rgName, nil
	}

	// add new node to default rg
	rm.groups[DefaultResourceGroupName].handleNodeUp(node)
	log.Info("HandleNodeUp: assign node to default resource group",
		zap.String("rgName", DefaultResourceGroupName),
		zap.Int64("node", node),
	)
	return DefaultResourceGroupName, nil
}

func (rm *ResourceManager) HandleNodeDown(node int64) (string, error) {
	rm.rwmutex.Lock()
	defer rm.rwmutex.Unlock()

	if rm.nodeMgr.Get(node) == nil {
		return "", ErrNodeNotExist
	}

	rgName, err := rm.findResourceGroupByNode(node)
	if err == nil {
		log.Info("HandleNodeDown: remove node from resource group",
			zap.String("rgName", rgName),
			zap.Int64("node", node),
		)
		return rgName, rm.groups[rgName].handleNodeDown(node)
	}

	return "", ErrNodeNotAssignToRG
}

func (rm *ResourceManager) TransferNode(from, to string) error {
	rm.rwmutex.Lock()
	defer rm.rwmutex.Unlock()

	if rm.groups[from] == nil || rm.groups[to] == nil {
		return ErrRGNotExist
	}

	if len(rm.groups[from].nodes) == 0 {
		return ErrRGIsEmpty
	}

	rm.checkRGNodeStatus(from)
	rm.checkRGNodeStatus(to)

	//todo: a better way to choose a node with least balance cost
	node := rm.groups[from].GetNodes()[0]
	if err := rm.transferNodeInStore(from, to, node); err != nil {
		return err
	}

	err := rm.groups[from].unassignNode(node)
	if err != nil {
		// interrupt transfer, unreachable logic path
		return err
	}

	err = rm.groups[to].assignNode(node)
	if err != nil {
		// interrupt transfer, unreachable logic path
		return err
	}

	return nil
}

func (rm *ResourceManager) transferNodeInStore(from string, to string, node int64) error {
	fromNodeList := make([]int64, 0)
	for nid := range rm.groups[from].nodes {
		if nid != node {
			fromNodeList = append(fromNodeList, nid)
		}
	}
	toNodeList := rm.groups[to].GetNodes()
	toNodeList = append(toNodeList, node)

	fromRG := &querypb.ResourceGroup{
		Name:     from,
		Capacity: int32(rm.groups[from].GetCapacity()) - 1,
		Nodes:    fromNodeList,
	}

	toRG := &querypb.ResourceGroup{
		Name:     to,
		Capacity: int32(rm.groups[to].GetCapacity()) + 1,
		Nodes:    toNodeList,
	}

	return rm.store.SaveResourceGroup(fromRG, toRG)
}

// auto recover rg, return recover used node num
func (rm *ResourceManager) AutoRecoverResourceGroup(rgName string) (int, error) {
	rm.rwmutex.Lock()
	defer rm.rwmutex.Unlock()

	if rm.groups[rgName] == nil {
		return 0, ErrRGNotExist
	}

	rm.checkRGNodeStatus(rgName)
	lackNodesNum := rm.groups[rgName].LackOfNodes()
	nodesInDefault := rm.groups[DefaultResourceGroupName].GetNodes()
	for i := 0; i < len(nodesInDefault) && i < lackNodesNum; i++ {
		//todo: a better way to choose a node with least balance cost
		node := nodesInDefault[i]
		err := rm.unassignNode(DefaultResourceGroupName, node)
		if err != nil {
			// interrupt transfer, unreachable logic path
			return i + 1, err
		}

		err = rm.groups[rgName].handleNodeUp(node)
		if err != nil {
			// roll back, unreachable logic path
			rm.assignNode(DefaultResourceGroupName, node)
		}
	}

	return lackNodesNum, nil
}

func (rm *ResourceManager) Recover() error {
	rm.rwmutex.Lock()
	defer rm.rwmutex.Unlock()
	rgs, err := rm.store.GetResourceGroups()
	if err != nil {
		return ErrRecoverResourceGroupToStore
	}

	for _, rg := range rgs {
		rm.groups[rg.GetName()] = NewResourceGroup(0)
		for _, node := range rg.GetNodes() {
			rm.groups[rg.GetName()].assignNode(node)
		}
		rm.checkRGNodeStatus(rg.GetName())
		log.Info("Recover resource group",
			zap.String("rgName", rg.GetName()),
			zap.Int64s("nodes", rg.GetNodes()),
			zap.Int32("capacity", rg.GetCapacity()),
		)
	}

	return nil
}

// every operation which involves nodes access, should check nodes status first
func (rm *ResourceManager) checkRGNodeStatus(rgName string) {
	for _, node := range rm.groups[rgName].GetNodes() {
		if rm.nodeMgr.Get(node) == nil {
			log.Info("found node down, remove it",
				zap.String("rgName", rgName),
				zap.Int64("nodeID", node),
			)

			rm.groups[rgName].handleNodeDown(node)
		}
	}
}

// return lack of nodes num
func (rm *ResourceManager) CheckLackOfNode(rgName string) int {
	rm.rwmutex.Lock()
	defer rm.rwmutex.Unlock()
	if rm.groups[rgName] == nil {
		return 0
	}

	rm.checkRGNodeStatus(rgName)

	return rm.groups[rgName].LackOfNodes()
}
