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
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/hashicorp/consul/api"

	"github.com/pkg/errors"

	"github.com/lexis-project/yorc-heappe-plugin/heappe"
	"github.com/ystia/yorc/v4/config"
	"github.com/ystia/yorc/v4/deployments"
	"github.com/ystia/yorc/v4/events"
	"github.com/ystia/yorc/v4/helper/consulutil"
	"github.com/ystia/yorc/v4/prov"
	"github.com/ystia/yorc/v4/prov/operations"
	"github.com/ystia/yorc/v4/storage"
	storageTypes "github.com/ystia/yorc/v4/storage/types"
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
	datasetReqConsulAttribute        = "input_dataset"
)

// SetLocationsExecution holds Locations computation properties
type SetLocationsExecution struct {
	KV             *api.KV
	Cfg            config.Configuration
	DeploymentID   string
	TaskID         string
	NodeName       string
	Token          string
	Operation      prov.Operation
	EnvInputs      []*operations.EnvInput
	VarInputsNames []string
}

// CloudRequirement holds a compute instance requirements
type CloudRequirement struct {
	NumCPUs        string `json:"num_cpus"`
	MemSize        string `json:"mem_size"`
	DiskSize       string `json:"disk_size"`
	OSType         string `json:"os_type"`
	OSDistribution string `json:"os_distribution"`
	OSVersion      string `json:"os_version"`
	Optional       string `json:"optional,omitempty"`
}

// CloudLocation holds properties of a cloud location to use
type CloudLocation struct {
	Name           string `json:"location_name"`
	Flavor         string `json:"flavor"`
	ImageID        string `json:"image_id"`
	FloatingIPPool string `json:"floating_ip_pool"`
	User           string `json:"user"`
}

// CloudLocation holds properties of a cloud location to use
type HPCLocation struct {
	Name           string `json:"location_name"`
	Flavor         string `json:"flavor"`
	ImageID        string `json:"image_id"`
	FloatingIPPool string `json:"floating_ip_pool"`
	User           string `json:"user"`
}

// DatasetRequirement holds an input requirements
type DatasetRequirement struct {
	Locations          []string `json:"locations"`
	NumberOfFiles      string   `json:"number_of_files"`
	NumberOfSmallFiles string   `json:"number_of_small_files"`
	Size               string   `json:"size"`
}

// ExecuteAsync is not supported here
func (e *SetLocationsExecution) ExecuteAsync(ctx context.Context) (*prov.Action, time.Duration, error) {
	return nil, 0, errors.Errorf("Unsupported asynchronous operation %s", e.Operation.Name)
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
		return e.setLocations(ctx)
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
		return e.addTarget(ctx)
	case tosca.RunnableSubmitOperationName, tosca.RunnableCancelOperationName:
		err = errors.Errorf("Unsupported operation %s", e.Operation.Name)
	default:
		err = errors.Errorf("Unsupported operation %s", e.Operation.Name)
	}

	return err
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

func (e *SetLocationsExecution) setLocations(ctx context.Context) error {

	var err error

	// Get requirements
	cloudReqs, err := e.getStoredCloudRequirements(ctx)
	if err != nil {
		return err
	}
	datasetReqs, err := e.getStoredDatasetRequirements(ctx)
	if err != nil {
		return err
	}

	hpcReqs, err := e.getStoredHPCRequirements(ctx)
	if err != nil {
		return err
	}

	// Compute locations fulfilling these requirements
	// TODO cloudLocations, hpcLocations, err := e.computeLocations(ctx, cloudReqs, hpcReqs, datasetReqs)
	cloudLocations, err := e.computeLocations(ctx, cloudReqs, hpcReqs, datasetReqs)
	if err != nil {
		return err
	}

	// Assign locations to cloud instances
	err = e.assignCloudLocations(ctx, cloudReqs, cloudLocations)
	if err != nil {
		return err
	}

	// Assign locations to HEAppE jobs
	// TODO err = e.assignHPCLocations(ctx, cloudReqs, cloudLocations)

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

	cloudreq, err := e.getStoredCloudRequirements(ctx)
	if err != nil {
		return err
	}

	// Add a new requirement
	newReq, err := e.getCloudRequirementFromEnvInputs()
	if err != nil {
		return err
	}
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
func (e *SetLocationsExecution) getStoredCloudRequirements(ctx context.Context) (map[string]CloudRequirement, error) {

	var cloudreq map[string]CloudRequirement
	ids, err := deployments.GetNodeInstancesIds(ctx, e.DeploymentID, e.NodeName)
	if err != nil {
		return cloudreq, err
	}

	if len(ids) == 0 {
		return cloudreq, errors.Errorf("Found no instance for node %s in deployment %s", e.NodeName, e.DeploymentID)
	}

	// Get already collected requirements
	attr, err := deployments.GetInstanceAttributeValue(ctx, e.DeploymentID, e.NodeName, ids[0], cloudReqConsulAttribute)
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

	hpcreq, err := e.getStoredHPCRequirements(ctx)
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
func (e *SetLocationsExecution) getHPCRequirementFromEnvInputs() (heappe.JobSpecification, error) {
	var req heappe.JobSpecification
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
func (e *SetLocationsExecution) getStoredHPCRequirements(ctx context.Context) (map[string]heappe.JobSpecification, error) {

	var hpcreq map[string]heappe.JobSpecification
	ids, err := deployments.GetNodeInstancesIds(ctx, e.DeploymentID, e.NodeName)
	if err != nil {
		return hpcreq, err
	}

	if len(ids) == 0 {
		return hpcreq, errors.Errorf("Found no instance for node %s in deployment %s", e.NodeName, e.DeploymentID)
	}

	// Get already collected requirements
	attr, err := deployments.GetInstanceAttributeValue(ctx, e.DeploymentID, e.NodeName, ids[0], hpcReqConsulAttribute)
	if err != nil {
		return hpcreq, err
	}

	if attr == nil || attr.RawString() == "" {
		hpcreq = make(map[string]heappe.JobSpecification)
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

	datasetReq, err := e.getStoredDatasetRequirements(ctx)
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
func (e *SetLocationsExecution) getStoredDatasetRequirements(ctx context.Context) (map[string]DatasetRequirement, error) {

	var datasetreq map[string]DatasetRequirement
	ids, err := deployments.GetNodeInstancesIds(ctx, e.DeploymentID, e.NodeName)
	if err != nil {
		return datasetreq, err
	}

	if len(ids) == 0 {
		return datasetreq, errors.Errorf("Found no instance for node %s in deployment %s", e.NodeName, e.DeploymentID)
	}

	// Get already collected requirements
	attr, err := deployments.GetInstanceAttributeValue(ctx, e.DeploymentID, e.NodeName, ids[0], datasetReqConsulAttribute)
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

func (e *SetLocationsExecution) computeLocations(ctx context.Context, cloudReqs map[string]CloudRequirement,
	hpcReqs map[string]heappe.JobSpecification, datasetReqs map[string]DatasetRequirement) (map[string]CloudLocation, error) {

	locations := make(map[string]CloudLocation)
	var err error

	// TODO: call dynamic allocation business logic
	// instead of using this test code
	datacenter := "lrz"
	for datasetName, datasetReq := range datasetReqs {
		locs := datasetReq.Locations
		if len(locs) > 0 {
			// Convention: the first section of location identify the datacenter
			datacenter = strings.ToLower(strings.SplitN(locs[0], "_", 2)[0])
			events.WithContextOptionalFields(ctx).NewLogEntry(events.LogLevelINFO, e.DeploymentID).Registerf(
				"Using datacenter %s for input dataset %s", datacenter, datasetName)
			break
		}
	}

	for nodeName, req := range cloudReqs {
		// Get user according to the version
		user := "centos"
		var imageID string
		distrib := strings.ToLower(req.OSDistribution)
		numCPUS := 2
		var floatingIPPool string
		var flavorName string
		if req.NumCPUs != "" {
			numCPUS, err = strconv.Atoi(req.NumCPUs)
			if err != nil {
				events.WithContextOptionalFields(ctx).NewLogEntry(events.LogLevelINFO, e.DeploymentID).Registerf(
					"[ERROR] Wrong number of CPUs for %s : %s, assuming it needs 2 CPUs", nodeName, req.NumCPUs)
			}
		}
		if distrib == "ubuntu" {
			user = "ubuntu"
		} else if distrib == "windows" {
			user = "Admin"
		}
		if datacenter == "it4i" {
			if distrib == "ubuntu" {
				imageID = "a619d8b0-e083-48b8-9c7e-e647324d1961"
			} else if distrib == "windows" {
				imageID = "94411a31-c407-4751-94c9-a4f4a963708d"
			} else {
				imageID = "768a2db5-1381-42be-894d-e4b496ca24b8"
			}

			switch numCPUS {
			case 0, 1, 2:
				flavorName = "normal"
			case 3, 4:
				flavorName = "large"
			default:
				flavorName = "xlarge-mem128"
			}

			floatingIPPool = "vlan104_lexis"
		} else {
			if distrib == "ubuntu" {
				imageID = "0d006427-aef5-4ed8-99c6-e381724a60e0"
			} else if distrib == "windows" {
				imageID = "2436a8b1-737c-429d-b4eb-e6b415bc9d02"
			} else {
				imageID = "9ac7db93-dbed-4a41-8e77-616024e48c2e"
			}

			switch numCPUS {
			case 0, 1, 2:
				flavorName = "lrz.medium"
			case 3, 4:
				flavorName = "lrz.large"
			default:
				flavorName = "lrz.xlarge"
			}

			floatingIPPool = "internet_pool"

		}

		locations[nodeName] = CloudLocation{
			Name:           datacenter,
			Flavor:         flavorName,
			ImageID:        imageID,
			FloatingIPPool: floatingIPPool,
			User:           user,
		}
	}
	// Store new collected requirements value
	err = deployments.SetAttributeComplexForAllInstances(ctx, e.DeploymentID, e.NodeName,
		cloudLocationsConsulAttribute, locations)
	if err != nil {
		err = errors.Wrapf(err, "Failed to store cloud locations results for deployment %s node %s",
			e.DeploymentID, e.NodeName)
		return locations, err
	}
	return locations, err

}

func (e *SetLocationsExecution) assignCloudLocations(ctx context.Context, requirements map[string]CloudRequirement, locations map[string]CloudLocation) error {

	var err error
	for nodeName, req := range requirements {
		location, ok := locations[nodeName]
		if !ok {
			optional, _ := strconv.ParseBool(req.Optional)
			if optional {
				events.WithContextOptionalFields(ctx).NewLogEntry(events.LogLevelINFO, e.DeploymentID).Registerf(
					"No available location for optional compute instance %s in deployment %s", nodeName, e.DeploymentID)
				err = e.setCloudLocationSkipped(ctx, nodeName)
				if err != nil {
					return err
				}
			} else {
				return errors.Errorf("No available location found for compute instance %s in deployment %s", nodeName, e.DeploymentID)
			}
		}
		events.WithContextOptionalFields(ctx).NewLogEntry(events.LogLevelINFO, e.DeploymentID).Registerf(
			"Location for %s: %s", nodeName, location.Name)

		err = e.setCloudLocation(ctx, nodeName, req, location)

	}
	return err
}

// setCloudLocation updates the deployment description of a compute instance for a new location
func (e *SetLocationsExecution) setCloudLocation(ctx context.Context, nodeName string, requirement CloudRequirement, location CloudLocation) error {

	nodeTemplate, err := e.getStoredNodeTemplate(ctx, nodeName)
	if err != nil {
		return err
	}

	// Add the new location in this node template metadata
	newLocationName := location.Name + "_openstack"
	if nodeTemplate.Metadata == nil {
		nodeTemplate.Metadata = make(map[string]string)
	}
	nodeTemplate.Metadata[tosca.MetadataLocationNameKey] = newLocationName
	// Update the flavor
	flavorVal := tosca.ValueAssignment{
		Type:  tosca.ValueAssignmentLiteral,
		Value: location.Flavor,
	}
	nodeTemplate.Properties["flavorName"] = &flavorVal

	// Update to boot volume image ID
	val, ok := nodeTemplate.Properties["boot_volume"]
	if !ok {
		return errors.Errorf("Found no boot volume defined for node %s in deployment %s", nodeName, e.DeploymentID)
	}
	bootVolume := val.GetMap()
	bootVolume["uuid"] = location.ImageID
	volumeVal := tosca.ValueAssignment{
		Type:  tosca.ValueAssignmentMap,
		Value: bootVolume,
	}
	nodeTemplate.Properties["boot_volume"] = &volumeVal

	// Update the user in credentials
	val, ok = nodeTemplate.Capabilities["endpoint"].Properties["credentials"]
	if !ok {
		return errors.Errorf("Found no credentials defined for node %s in deployment %s", nodeName, e.DeploymentID)
	}
	creds := val.GetMap()
	creds["user"] = location.User
	credsVal := tosca.ValueAssignment{
		Type:  tosca.ValueAssignmentMap,
		Value: creds,
	}

	nodeTemplate.Capabilities["endpoint"].Properties["credentials"] = &credsVal

	// Location is now changed for this node template, storing it
	err = e.storeNodeTemplate(ctx, nodeName, nodeTemplate)
	if err != nil {
		return err
	}

	// Update the associated Floating IP Node location
	var floatingIPNodeName string
	for _, nodeReq := range nodeTemplate.Requirements {
		for _, reqAssignment := range nodeReq {
			if reqAssignment.Capability == fipConnectivityCapability {
				floatingIPNodeName = reqAssignment.Node
				break
			}
		}
	}
	if floatingIPNodeName == "" {
		// No associated floating IP pool to change, locations changes are done now
		events.WithContextOptionalFields(ctx).NewLogEntry(events.LogLevelINFO, e.DeploymentID).Registerf(
			"No floating IP associated to compute instance %s in deployment %s", nodeName, e.DeploymentID)
		return err
	}

	fipNodeTemplate, err := e.getStoredNodeTemplate(ctx, floatingIPNodeName)
	if err != nil {
		return err
	}
	if fipNodeTemplate.Metadata == nil {
		fipNodeTemplate.Metadata = make(map[string]string)
	}
	fipNodeTemplate.Metadata[tosca.MetadataLocationNameKey] = newLocationName

	// Update as well the Floating IP pool
	poolVal := tosca.ValueAssignment{
		Type:  tosca.ValueAssignmentLiteral,
		Value: location.FloatingIPPool,
	}
	fipNodeTemplate.Properties["floating_network_name"] = &poolVal

	// Location is now changed for this node template, storing it
	err = e.storeNodeTemplate(ctx, floatingIPNodeName, fipNodeTemplate)
	if err != nil {
		return err
	}

	events.WithContextOptionalFields(ctx).NewLogEntry(events.LogLevelINFO, e.DeploymentID).Registerf(
		"Floating IP pool is %s for %s in deployment %s", location.FloatingIPPool, floatingIPNodeName, e.DeploymentID)

	return err
}

// setCloudLocation updates the deployment description of a compute instance that has to be skipped
func (e *SetLocationsExecution) setCloudLocationSkipped(ctx context.Context, nodeName string) error {
	return errors.Errorf("Skipping a cloud compute instance without location not yet implemented")
}

// getStoredNodeTemplate returns the description of a node stored by Yorc
func (e *SetLocationsExecution) getStoredNodeTemplate(ctx context.Context, nodeName string) (*tosca.NodeTemplate, error) {
	node := new(tosca.NodeTemplate)
	nodePath := path.Join(consulutil.DeploymentKVPrefix, e.DeploymentID, "topology", "nodes", nodeName)
	found, err := storage.GetStore(storageTypes.StoreTypeDeployment).Get(nodePath, node)
	if !found {
		err = errors.Errorf("No such node %s in deployment %s", nodeName, e.DeploymentID)
	}
	return node, err
}

// storeNodeTemplate stores a node template in Yorc
func (e *SetLocationsExecution) storeNodeTemplate(ctx context.Context, nodeName string, nodeTemplate *tosca.NodeTemplate) error {
	nodePrefix := path.Join(consulutil.DeploymentKVPrefix, e.DeploymentID, "topology", "nodes", nodeName)
	return storage.GetStore(storageTypes.StoreTypeDeployment).Set(ctx, nodePrefix, nodeTemplate)
}
