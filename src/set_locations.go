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
	cloudTargetRelationship         = "org.lexis.common.dynamic.orchestration.relationships.CloudResource"
	optionalCloudTargetRelationship = "org.lexis.common.dynamic.orchestration.relationships.OptionalCloudResource"
	fipConnectivityCapability       = "yorc.capabilities.openstack.FIPConnectivity"
	cloudReqConsulAttrbute          = "cloud_requirements"
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

// CloudRequirement holds properties of a cloud location to use
type CloudLocation struct {
	Name           string `json:"location_name"`
	Flavor         string `json:"flavor"`
	ImageID        string `json:"image_id"`
	FloatingIPPool string `json:"floating_ip_pool"`
	User           string `json:"user"`
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
	requirements, err := e.getStoredCloudRequirements(ctx)
	if err != nil {
		return err
	}

	// Compute locations fulfilling these requirements
	locations, err := e.computeLocations(ctx, requirements)
	if err != nil {
		return err
	}

	// Assign locations to instances
	err = e.assignCloudLocations(ctx, requirements, locations)

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
		cloudReqConsulAttrbute, cloudreq)
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
	attr, err := deployments.GetInstanceAttributeValue(ctx, e.DeploymentID, e.NodeName, ids[0], cloudReqConsulAttrbute)
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

func (e *SetLocationsExecution) computeLocations(ctx context.Context, requirements map[string]CloudRequirement) (map[string]CloudLocation, error) {
	locations := make(map[string]CloudLocation)
	var err error

	// TODO: call dynamic allocation business logic
	for nodeName, req := range requirements {
		// Get user according to the version
		user := "centos"
		imageID := "9ac7db93-dbed-4a41-8e77-616024e48c2e"
		distrib := strings.ToLower(req.OSDistribution)
		if distrib == "ubuntu" {
			user = "ubuntu"
			imageID = "8aecf074-8bfe-40ce-9de4-eb260351adb8"
		} else if distrib == "windows" {
			user = "Admin"
			imageID = "2436a8b1-737c-429d-b4eb-e6b415bc9d02"
		}

		locations[nodeName] = CloudLocation{
			Name:           "lrz",
			Flavor:         "lrz.medium",
			ImageID:        imageID,
			FloatingIPPool: "MWN_pool",
			User:           user,
		}
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

	newLocationName := "openstack_" + location.Name
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
