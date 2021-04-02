// Copyright 2021 Bull S.A.S. Atos Technologies - Bull, Rue Jean Jaures, B.P.68, 78340, Les Clayes-sous-Bois, France.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/hashicorp/consul/api"

	"github.com/pkg/errors"

	"github.com/ystia/yorc/v4/config"
	"github.com/ystia/yorc/v4/deployments"
	"github.com/ystia/yorc/v4/events"
	"github.com/ystia/yorc/v4/helper/consulutil"
	"github.com/ystia/yorc/v4/prov"
	"github.com/ystia/yorc/v4/prov/operations"
	"github.com/ystia/yorc/v4/tosca"
)

const (
	cloudTargetRelationship          = "org.lexis.common.dynamic.orchestration.relationships.CloudResource"
	optionalCloudTargetRelationship  = "org.lexis.common.dynamic.orchestration.relationships.OptionalCloudResource"
	heappeTargetRelationship         = "org.lexis.common.dynamic.orchestration.relationships.HeappeJob"
	optionalHEAppETargetRelationship = "org.lexis.common.dynamic.orchestration.relationships.OptionalHeappeJob"
	datasetTargetRelationship        = "org.lexis.common.dynamic.orchestration.relationships.Dataset"
	fipConnectivityCapability        = "yorc.capabilities.openstack.FIPConnectivity"
	cloudReqConsulAttribute          = "cloud_requirements"
	hpcReqConsulAttribute            = "heappe_job"
	cloudLocationsConsulAttribute    = "cloud_locations"
	hpcLocationsConsulAttribute      = "hpc_locations"
	datasetReqConsulAttribute        = "input_dataset"
	osCapability                     = "tosca.capabilities.OperatingSystem"
	heappeJobCapability              = "org.lexis.common.heappe.capabilities.HeappeJob"
	datasetInfoCapability            = "org.lexis.common.ddi.capabilities.DatasetInfo"
	hostCapabilityName               = "host"
	osCapabilityName                 = "os"
	datasetInfoCapabilityName        = "dataset_info"
)

// SetLocationsExecution holds Locations computation properties
type SetLocationsExecution struct {
	KV                     *api.KV
	Cfg                    config.Configuration
	DeploymentID           string
	TaskID                 string
	NodeName               string
	Token                  string
	Operation              prov.Operation
	EnvInputs              []*operations.EnvInput
	VarInputsNames         []string
	MonitoringTimeInterval time.Duration
}

// CloudRequirement holds a compute instance requirements
type CloudRequirement struct {
	NumCPUs        string `json:"num_cpus"`
	MemSize        string `json:"mem_size"`
	DiskSize       string `json:"disk_size"`
	OSType         string `json:"os_type"`
	OSDistribution string `json:"os_distribution"`
	OSVersion      string `json:"os_version"`
	Optional       bool   `json:"optional,omitempty"`
}

// TaskParalizationParameter holds paramters of tasks parallelization read from a json value providing strings instead of int
type TaskParalizationParameter struct {
	MPIProcesses  int `json:"MPIProcesses,string"`
	OpenMPThreads int `json:"OpenMPThreads,string"`
	MaxCores      int `json:"MaxCores,string"`
}

// TaskSpecification holds task properties read from a json value providing strings instead of int
type TaskSpecification struct {
	Name                       string
	MinCores                   int      `json:"MinCores,string"`
	MaxCores                   int      `json:"MaxCores,string"`
	WalltimeLimit              int      `json:"WalltimeLimit,string"`
	RequiredNodes              []string `json:"RequiredNodes,omitempty"`
	Priority                   int      `json:"Priority,string"`
	JobArrays                  string
	IsExclusive                bool                        `json:"IsExclusive,string"`
	IsRerunnable               bool                        `json:"IsRerunnable,string"`
	CpuHyperThreading          bool                        `json:"CpuHyperThreading,string"`
	ClusterNodeTypeID          int                         `json:"ClusterNodeTypeId,string"`
	CommandTemplateID          int                         `json:"CommandTemplateId,string"`
	TaskParalizationParameters []TaskParalizationParameter `json:"TaskParalizationParameters,omitempty"`
}

// JobSpecification holds job properties read from a json value providing strings instead of int
type JobSpecification struct {
	Name         string
	Project      string
	WaitingLimit int `json:"WaitingLimit,string"`
	ClusterID    int `json:"ClusterId,string"`
	Tasks        []TaskSpecification
}

// HPCRequirement holds a HPC job requirements
type HPCRequirement struct {
	*JobSpecification
	Optional bool `json:"optional,omitempty"`
}

// CloudLocation holds properties of a cloud location to use
type CloudLocation struct {
	Name           string `json:"location_name"`
	Flavor         string `json:"flavor"`
	ImageID        string `json:"image_id"`
	FloatingIPPool string `json:"floating_ip_pool"`
	User           string `json:"user"`
}

// TaskLocation holds properties of a task
type TaskLocation struct {
	NodeTypeID        int `json:"cluster_node_type_id"`
	CommandTemplateID int `json:"command_template_id"`
}

// HPCLocation holds properties of a cloud location to use
type HPCLocation struct {
	Name          string                  `json:"location_name"`
	Project       string                  `json:"project_name"`
	TasksLocation map[string]TaskLocation `json:"tasks_location"`
}

// DatasetRequirement holds an input requirements
type DatasetRequirement struct {
	Locations          []string `json:"locations"`
	NumberOfFiles      string   `json:"number_of_files"`
	NumberOfSmallFiles string   `json:"number_of_small_files"`
	Size               string   `json:"size"`
}

func (e *SetLocationsExecution) ExecuteAsync(ctx context.Context) (*prov.Action, time.Duration, error) {
	if strings.ToLower(e.Operation.Name) != tosca.RunnableRunOperationName {
		return nil, 0, errors.Errorf("Unsupported asynchronous operation %q", e.Operation.Name)
	}

	requestID, err := e.getRequestID(ctx)
	if err != nil {
		return nil, 0, err
	}

	data := make(map[string]string)
	data[actionDataTaskID] = e.TaskID
	data[actionDataNodeName] = e.NodeName
	data[actionDataRequestID] = requestID
	data[actionDataToken] = e.Token

	return &prov.Action{ActionType: computeBestLocationAction, Data: data}, e.MonitoringTimeInterval, err
}

// Execute executes a synchronous operation
func (e *SetLocationsExecution) Execute(ctx context.Context) error {

	var err error
	switch strings.ToLower(e.Operation.Name) {
	case "install", "standard.create":
		events.WithContextOptionalFields(ctx).NewLogEntry(events.LogLevelINFO, e.DeploymentID).Registerf(
			"Creating %q", e.NodeName)
		// Nothing to do here
	case "standard.start":
		events.WithContextOptionalFields(ctx).NewLogEntry(events.LogLevelINFO, e.DeploymentID).Registerf(
			"Starting %q", e.NodeName)
		// Nothing to do here
	case "uninstall", "standard.delete":
		events.WithContextOptionalFields(ctx).NewLogEntry(events.LogLevelINFO, e.DeploymentID).Registerf(
			"Deleting %q", e.NodeName)
		// Nothing to do here
	case "standard.stop":
		// Nothing to do
		events.WithContextOptionalFields(ctx).NewLogEntry(events.LogLevelINFO, e.DeploymentID).Registerf(
			"Executing operation %s on node %q", e.Operation.Name, e.NodeName)
	case "configure.pre_configure_source":
		events.WithContextOptionalFields(ctx).NewLogEntry(events.LogLevelINFO, e.DeploymentID).Registerf(
			"New target for %q", e.NodeName)
		// return e.addTarget(ctx)
	case tosca.RunnableSubmitOperationName:
		events.WithContextOptionalFields(ctx).NewLogEntry(events.LogLevelINFO, e.DeploymentID).Registerf(
			"%s submitting request to compute best location by", e.NodeName)
		err = e.submitComputeBestLocationRequest(ctx)
		if err != nil {
			events.WithContextOptionalFields(ctx).NewLogEntry(events.LogLevelINFO, e.DeploymentID).Registerf(
				"Failed to submit data transfer for node %q, error %s", e.NodeName, err.Error())

		}
	case tosca.RunnableCancelOperationName:
		events.WithContextOptionalFields(ctx).NewLogEntry(events.LogLevelINFO, e.DeploymentID).Registerf(
			"Canceling Job %q", e.NodeName)
		/*
			err = e.cancelJob(ctx)
			if err != nil {
				events.WithContextOptionalFields(ctx).NewLogEntry(events.LogLevelINFO, e.DeploymentID).Registerf(
					"Failed to cancel Job %q, error %s", e.NodeName, err.Error())
			}
		*/
		err = errors.Errorf("Unsupported operation %q", e.Operation.Name)
	default:
		err = errors.Errorf("Unsupported operation %s", e.Operation.Name)
	}

	return err
}

func (e *SetLocationsExecution) submitComputeBestLocationRequest(ctx context.Context) error {

	// Find associated targets for which to update the locations
	err := e.findAssociatedTargets(ctx)
	if err != nil {
		return err
	}

	// TODO: call the Business logic
	requestID := "test_request_id"
	// Store the request id
	err = deployments.SetAttributeForAllInstances(ctx, e.DeploymentID, e.NodeName,
		requestIDConsulAttribute, requestID)
	if err != nil {
		return errors.Wrapf(err, "Request %s submitted, but failed to store this request id", requestID)
	}
	return err
}

func (e *SetLocationsExecution) getRequestID(ctx context.Context) (string, error) {

	val, err := deployments.GetInstanceAttributeValue(ctx, e.DeploymentID, e.NodeName, "0", requestIDConsulAttribute)
	if err != nil {
		return "", errors.Wrapf(err, "Failed to get request ID for deployment %s node %s", e.DeploymentID, e.NodeName)
	} else if val == nil {
		return "", errors.Errorf("Found no request id for deployment %s node %s", e.DeploymentID, e.NodeName)
	}

	return val.RawString(), err
}

// ResolveExecution resolves inputs before the execution of an operation
func (e *SetLocationsExecution) ResolveExecution(ctx context.Context) error {
	return e.resolveInputs(ctx)
}

func (e *SetLocationsExecution) resolveInputs(ctx context.Context) error {
	var err error
	e.EnvInputs, e.VarInputsNames, err = operations.ResolveInputsWithInstances(
		ctx, e.DeploymentID, e.NodeName, e.TaskID, e.Operation, nil, nil)
	return err
}

func (e *SetLocationsExecution) addTarget(ctx context.Context) error {

	isCloudTarget, err := deployments.IsTypeDerivedFrom(ctx, e.DeploymentID,
		e.Operation.ImplementedInType, cloudTargetRelationship)
	if err != nil {
		return err
	}
	if isCloudTarget {
		isOptionalTarget, err := deployments.IsTypeDerivedFrom(ctx, e.DeploymentID,
			e.Operation.ImplementedInType, optionalCloudTargetRelationship)
		if err != nil {
			return err
		}
		return e.addCloudTarget(ctx, isOptionalTarget)
	}

	isHPCTarget, err := deployments.IsTypeDerivedFrom(ctx, e.DeploymentID,
		e.Operation.ImplementedInType, heappeTargetRelationship)
	if err != nil {
		return err
	}
	if isHPCTarget {
		isOptionalTarget, err := deployments.IsTypeDerivedFrom(ctx, e.DeploymentID,
			e.Operation.ImplementedInType, optionalHEAppETargetRelationship)
		if err != nil {
			return err
		}
		return e.addHPCTarget(ctx, isOptionalTarget)
	}

	isDatasetTarget, err := deployments.IsTypeDerivedFrom(ctx, e.DeploymentID,
		e.Operation.ImplementedInType, datasetTargetRelationship)
	if err != nil {
		return err
	}
	if isDatasetTarget {
		return e.addDatasetTarget(ctx)
	}
	return err
}

// findAssociatedTarget finds which compute instances, datasets and HPC jobs are
// associated to this component
func (e *SetLocationsExecution) findAssociatedTargets(ctx context.Context) error {
	nodeTemplate, err := getStoredNodeTemplate(ctx, e.DeploymentID, e.NodeName)
	if err != nil {
		return err
	}

	// Get the associated targets
	cloudReqs := make(map[string]CloudRequirement)
	hpcReqs := make(map[string]HPCRequirement)
	datasetReqs := make(map[string]DatasetRequirement)
	for _, nodeReq := range nodeTemplate.Requirements {
		for _, reqAssignment := range nodeReq {
			switch reqAssignment.Capability {
			case osCapability:
				req, err := e.getCloudRequirement(ctx, reqAssignment.Node)
				if err != nil {
					return err
				}
				req.Optional = (reqAssignment.Relationship == optionalCloudTargetRelationship)
				cloudReqs[reqAssignment.Node] = req
			case heappeJobCapability:
				req, err := e.getHPCRequirement(ctx, reqAssignment.Node)
				if err != nil {
					return err
				}
				req.Optional = (reqAssignment.Relationship == optionalHEAppETargetRelationship)
				hpcReqs[reqAssignment.Node] = req
			case datasetInfoCapability:
				req, err := e.getDatasetRequirement(ctx, reqAssignment.Node)
				if err != nil {
					return err
				}
				datasetReqs[reqAssignment.Node] = req

			default:
				// Ignoring
			}
		}
	}

	// Store collected requirements
	err = deployments.SetAttributeComplexForAllInstances(ctx, e.DeploymentID, e.NodeName,
		cloudReqConsulAttribute, cloudReqs)
	if err != nil {
		err = errors.Wrapf(err, "Failed to store cloud requirement details for deployment %s node %s",
			e.DeploymentID, e.NodeName)
		return err
	}
	err = deployments.SetAttributeComplexForAllInstances(ctx, e.DeploymentID, e.NodeName,
		hpcReqConsulAttribute, hpcReqs)
	if err != nil {
		err = errors.Wrapf(err, "Failed to store HPC requirement details for deployment %s node %s",
			e.DeploymentID, e.NodeName)
		return err
	}
	err = deployments.SetAttributeComplexForAllInstances(ctx, e.DeploymentID, e.NodeName,
		datasetReqConsulAttribute, datasetReqs)
	if err != nil {
		err = errors.Wrapf(err, "Failed to store dataset requirement details for deployment %s node %s",
			e.DeploymentID, e.NodeName)
		return err
	}

	return err
}

// getCloudRequirement finds requirements of a cloud compute instance
func (e *SetLocationsExecution) getCloudRequirement(ctx context.Context, targetName string) (CloudRequirement, error) {
	var cloudReq CloudRequirement
	var err error

	// Get host capability properties
	var stringPropNames = []struct {
		field    *string
		propName string
	}{
		{field: &(cloudReq.NumCPUs), propName: "num_cpus"},
		{field: &(cloudReq.MemSize), propName: "mem_size"},
		{field: &(cloudReq.DiskSize), propName: "disk_size"},
	}
	for _, stringPropName := range stringPropNames {
		val, err := deployments.GetCapabilityPropertyValue(ctx, e.DeploymentID,
			targetName, hostCapabilityName, stringPropName.propName)
		if err != nil {
			return cloudReq, err
		}
		if val != nil {
			*(stringPropName.field) = val.RawString()
		}
	}

	// Get os capability properties
	stringPropNames = []struct {
		field    *string
		propName string
	}{
		{field: &(cloudReq.OSType), propName: "type"},
		{field: &(cloudReq.OSDistribution), propName: "distribution"},
		{field: &(cloudReq.OSVersion), propName: "version"},
	}
	for _, stringPropName := range stringPropNames {
		val, err := deployments.GetCapabilityPropertyValue(ctx, e.DeploymentID,
			targetName, osCapabilityName, stringPropName.propName)
		if err != nil {
			return cloudReq, err
		}
		if val != nil {
			*(stringPropName.field) = val.RawString()
		}
	}

	return cloudReq, err
}

// getHPCRequirement finds requirements of a cloud compute instance
func (e *SetLocationsExecution) getHPCRequirement(ctx context.Context, targetName string) (HPCRequirement, error) {
	var hpcReq HPCRequirement
	val, err := deployments.GetNodePropertyValue(ctx, e.DeploymentID, targetName, "JobSpecification")
	if err != nil {
		return hpcReq, err
	}
	if val != nil {
		err = json.Unmarshal([]byte(val.RawString()), &hpcReq)
		if err != nil {
			err = errors.Wrapf(err, "Failed to unmarshal heappe job from string %s", val.RawString())
		}
	}

	return hpcReq, err
}

// getDatasetRequirement finds requirements of a dataset
func (e *SetLocationsExecution) getDatasetRequirement(ctx context.Context, targetName string) (DatasetRequirement, error) {
	var datasetReq DatasetRequirement
	var err error

	ids, err := deployments.GetNodeInstancesIds(ctx, e.DeploymentID, targetName)
	if err != nil {
		return datasetReq, err
	}

	// Get string properties
	var stringPropNames = []struct {
		field    *string
		propName string
	}{
		{field: &(datasetReq.Size), propName: "size"},
		{field: &(datasetReq.NumberOfFiles), propName: "number_of_files"},
		{field: &(datasetReq.NumberOfSmallFiles), propName: "number_of_small_files"},
	}
	for _, stringPropName := range stringPropNames {
		val, err := deployments.GetInstanceCapabilityAttributeValue(ctx, e.DeploymentID,
			targetName, ids[0], datasetInfoCapabilityName, stringPropName.propName)
		if err != nil {
			return datasetReq, err
		}
		if val != nil {
			*(stringPropName.field) = val.RawString()
		}

	}

	// Get locations property
	val, err := deployments.GetInstanceCapabilityAttributeValue(ctx, e.DeploymentID,
		targetName, ids[0], datasetInfoCapabilityName, "locations")
	if err != nil {
		return datasetReq, err
	}
	if val != nil {
		err = json.Unmarshal([]byte(val.RawString()), &datasetReq.Locations)
		if err != nil {
			err = errors.Wrapf(err, "Failed to unmarshal locations from string %s", val.RawString())
		}
	}

	return datasetReq, err
}

// addCloudTarget is called when a new target is associated to this SetLocation component
func (e *SetLocationsExecution) addCloudTarget(ctx context.Context, optional bool) error {

	consulClient, err := e.Cfg.GetConsulClient()
	if err != nil {
		return err
	}
	lock, err := consulutil.AcquireLock(consulClient, ".lexis_lock_cloud_requirements", 0)
	if err != nil {
		return err
	}
	defer func() {
		_ = lock.Unlock()
	}()

	cloudreq, err := getStoredCloudRequirements(ctx, e.DeploymentID, e.NodeName)
	if err != nil {
		return err
	}

	// Add a new requirement
	newReq, err := e.getCloudRequirementFromEnvInputs()
	if err != nil {
		return err
	}
	newReq.Optional = optional
	cloudreq[e.Operation.RelOp.TargetNodeName] = newReq

	// Store new collected requirements value
	err = deployments.SetAttributeComplexForAllInstances(ctx, e.DeploymentID, e.NodeName,
		cloudReqConsulAttribute, cloudreq)
	if err != nil {
		err = errors.Wrapf(err, "Failed to store cloud requirement details for deployment %s node %s",
			e.DeploymentID, e.NodeName)
		return err
	}

	return err
}

// getCloudRequirementFromEnvInputs gets the relationship operation input parameters
func (e *SetLocationsExecution) getCloudRequirementFromEnvInputs() (CloudRequirement, error) {
	var req CloudRequirement
	var err error
	for _, envInput := range e.EnvInputs {
		switch envInput.Name {
		case "NUM_CPUS":
			req.NumCPUs = envInput.Value
		case "MEM_SIZE":
			req.MemSize = envInput.Value
		case "DISK_SIZE":
			req.DiskSize = envInput.Value
		case "OS_TYPE":
			req.OSType = envInput.Value
		case "OS_DISTRIBUTION":
			req.OSDistribution = envInput.Value
		case "OS_VERSION":
			req.OSVersion = envInput.Value

		default:
			// Not a requirement on cloud instance resource
		}
	}

	return req, err
}

// getStoredCloudRequirements retrieves cloud requirements already computed and
// stored by Yorc
func getStoredCloudRequirements(ctx context.Context,
	deploymentID, nodeName string) (map[string]CloudRequirement, error) {

	var cloudreq map[string]CloudRequirement
	ids, err := deployments.GetNodeInstancesIds(ctx, deploymentID, nodeName)
	if err != nil {
		return cloudreq, err
	}

	if len(ids) == 0 {
		return cloudreq, errors.Errorf("Found no instance for node %s in deployment %s", nodeName, deploymentID)
	}

	// Get already collected requirements
	attr, err := deployments.GetInstanceAttributeValue(ctx, deploymentID, nodeName, ids[0], cloudReqConsulAttribute)
	if err != nil {
		return cloudreq, err
	}

	if attr == nil || attr.RawString() == "" {
		cloudreq = make(map[string]CloudRequirement)
	} else {
		err = json.Unmarshal([]byte(attr.RawString()), &cloudreq)
		if err != nil {
			return cloudreq, errors.Wrapf(err, "Failed to unmarshal %s", attr.RawString())
		}
	}

	return cloudreq, err
}

// addHPCTarget is called when a new target is associated to this SetLocation component
func (e *SetLocationsExecution) addHPCTarget(ctx context.Context, optional bool) error {

	consulClient, err := e.Cfg.GetConsulClient()
	if err != nil {
		return err
	}
	lock, err := consulutil.AcquireLock(consulClient, ".lexis_lock_cloud_requirements", 0)
	if err != nil {
		return err
	}
	defer func() {
		_ = lock.Unlock()
	}()

	hpcreq, err := getStoredHPCRequirements(ctx, e.DeploymentID, e.NodeName)
	if err != nil {
		return err
	}

	// Add a new requirement
	newReq, err := e.getHPCRequirementFromEnvInputs()
	if err != nil {
		return err
	}
	hpcreq[e.Operation.RelOp.TargetNodeName] = newReq

	// Store new collected requirements value
	err = deployments.SetAttributeComplexForAllInstances(ctx, e.DeploymentID, e.NodeName,
		hpcReqConsulAttribute, hpcreq)
	if err != nil {
		err = errors.Wrapf(err, "Failed to store HPC requirement details for deployment %s node %s",
			e.DeploymentID, e.NodeName)
		return err
	}

	return err
}

// getHPCRequirementFromEnvInputs gets the relationship operation input parameters
func (e *SetLocationsExecution) getHPCRequirementFromEnvInputs() (HPCRequirement, error) {
	var req HPCRequirement
	var err error
	for _, envInput := range e.EnvInputs {
		switch envInput.Name {
		case "JOB_SPECIFICATION":
			err := json.Unmarshal([]byte(envInput.Value), &req)
			if err != nil {
				err = errors.Wrapf(err, "Failed to unmarshal heappe job from string %s", envInput.Value)
			}
			return req, err
		default:
			// Not a requirement on cloud instance resource
		}
	}

	return req, err
}

// getStoredHPCRequirements retrieves HPC requirements already computed and
// stored by Yorc
func getStoredHPCRequirements(ctx context.Context,
	deploymentID, nodeName string) (map[string]HPCRequirement, error) {

	var hpcreq map[string]HPCRequirement
	ids, err := deployments.GetNodeInstancesIds(ctx, deploymentID, nodeName)
	if err != nil {
		return hpcreq, err
	}

	if len(ids) == 0 {
		return hpcreq, errors.Errorf("Found no instance for node %s in deployment %s", nodeName, deploymentID)
	}

	// Get already collected requirements
	attr, err := deployments.GetInstanceAttributeValue(ctx, deploymentID, nodeName, ids[0], hpcReqConsulAttribute)
	if err != nil {
		return hpcreq, err
	}

	if attr == nil || attr.RawString() == "" {
		hpcreq = make(map[string]HPCRequirement)
	} else {
		err = json.Unmarshal([]byte(attr.RawString()), &hpcreq)
		if err != nil {
			return hpcreq, errors.Wrapf(err, "Failed to unmarshal %s", attr.RawString())
		}
	}

	return hpcreq, err
}

// addDatasetTarget is called when a new input dataset is associated to this SetLocation component
func (e *SetLocationsExecution) addDatasetTarget(ctx context.Context) error {

	consulClient, err := e.Cfg.GetConsulClient()
	if err != nil {
		return err
	}
	lock, err := consulutil.AcquireLock(consulClient, ".lexis_lock_cloud_requirements", 0)
	if err != nil {
		return err
	}
	defer func() {
		_ = lock.Unlock()
	}()

	datasetReq, err := getStoredDatasetRequirements(ctx, e.DeploymentID, e.NodeName)
	if err != nil {
		return err
	}

	// Add a new requirement
	newReq, err := e.getDatasetRequirementFromEnvInputs()
	if err != nil {
		return err
	}
	datasetReq[e.Operation.RelOp.TargetNodeName] = newReq

	// Store new collected requirements value
	err = deployments.SetAttributeComplexForAllInstances(ctx, e.DeploymentID, e.NodeName,
		datasetReqConsulAttribute, datasetReq)
	if err != nil {
		err = errors.Wrapf(err, "Failed to store dataset requirement details for deployment %s node %s",
			e.DeploymentID, e.NodeName)
		return err
	}

	return err
}

// getStoredDatasetRequirements retrieves dataset requirements already computed and
// stored by Yorc
func getStoredDatasetRequirements(ctx context.Context,
	deploymentID, nodeName string) (map[string]DatasetRequirement, error) {

	var datasetreq map[string]DatasetRequirement
	ids, err := deployments.GetNodeInstancesIds(ctx, deploymentID, nodeName)
	if err != nil {
		return datasetreq, err
	}

	if len(ids) == 0 {
		return datasetreq, errors.Errorf("Found no instance for node %s in deployment %s", nodeName, deploymentID)
	}

	// Get already collected requirements
	attr, err := deployments.GetInstanceAttributeValue(ctx, deploymentID, nodeName, ids[0], datasetReqConsulAttribute)
	if err != nil {
		return datasetreq, err
	}

	if attr == nil || attr.RawString() == "" {
		datasetreq = make(map[string]DatasetRequirement)
	} else {
		err = json.Unmarshal([]byte(attr.RawString()), &datasetreq)
		if err != nil {
			return datasetreq, errors.Wrapf(err, "Failed to unmarshal dataset requirements %s", attr.RawString())
		}
	}

	return datasetreq, err
}

// getDatasetRequirementFromEnvInputs gets the relationship operation input parameters
func (e *SetLocationsExecution) getDatasetRequirementFromEnvInputs() (DatasetRequirement, error) {
	var req DatasetRequirement
	var err error
	for _, envInput := range e.EnvInputs {
		switch envInput.Name {
		case "LOCATIONS":
			var sliceVal []string
			err = json.Unmarshal([]byte(envInput.Value), &sliceVal)
			if err != nil {
				return req, errors.Wrapf(err, "Failed to unmarshal %s", envInput.Value)
			}
			req.Locations = sliceVal
		case "SIZE":
			req.Size = envInput.Value
		case "NUMBER_OF_FILES":
			req.NumberOfFiles = envInput.Value
		case "NUMBER_OF_SMALL_FILES":
			req.NumberOfSmallFiles = envInput.Value
		default:
			// Not a requirement on input dataset
		}
	}

	return req, err
}
