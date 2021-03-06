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
	"fmt"
	"path"
	"strconv"
	"strings"

	"github.com/pkg/errors"
	"github.com/ystia/yorc/v4/config"
	"github.com/ystia/yorc/v4/deployments"
	"github.com/ystia/yorc/v4/events"
	"github.com/ystia/yorc/v4/helper/consulutil"
	"github.com/ystia/yorc/v4/log"
	"github.com/ystia/yorc/v4/prov"
	"github.com/ystia/yorc/v4/storage"
	storageTypes "github.com/ystia/yorc/v4/storage/types"
	"github.com/ystia/yorc/v4/tosca"
)

const (
	// TaskStatusPendingMsg is the message returned when a task is pending
	TaskStatusPendingMsg = "Task still in the queue, or task does not exist"
	// TaskStatusInProgressMsg is the message returned when a task is in progress
	TaskStatusInProgressMsg = "In progress"
	// TaskStatusDoneMsg is the message returned when a task is done
	TaskStatusDoneMsg = "Done"

	requestIDConsulAttribute = "request_id"
	requestStatusPending     = "PENDING"
	requestStatusRunning     = "RUNNING"
	requestStatusCompleted   = "COMPLETED"

	// computeBestLocationAction is the action of computing the best location
	computeBestLocationAction = "compute-best-location"

	actionDataNodeName  = "nodeName"
	actionDataRequestID = "requestID"
	actionDataTaskID    = "taskID"
)

// ActionOperator holds function allowing to execute an action
type ActionOperator struct {
}

type actionData struct {
	taskID   string
	nodeName string
}

// ExecAction allows to execute and action
func (o *ActionOperator) ExecAction(ctx context.Context, cfg config.Configuration, taskID, deploymentID string, action *prov.Action) (bool, error) {
	log.Debugf("Execute Action with ID:%q, taskID:%q, deploymentID:%q", action.ID, taskID, deploymentID)

	var deregister bool
	var err error
	if action.ActionType == computeBestLocationAction {
		deregister, err = o.monitorJob(ctx, cfg, deploymentID, action)
	} else {
		deregister = true
		err = errors.Errorf("Unsupported actionType %q", action.ActionType)
	}
	return deregister, err
}

func (o *ActionOperator) monitorJob(ctx context.Context, cfg config.Configuration, deploymentID string, action *prov.Action) (bool, error) {
	var deregister bool

	actionData, err := o.getActionData(action)
	if err != nil {
		return true, err
	}
	requestID, ok := action.Data[actionDataRequestID]
	if !ok {
		return true, errors.Errorf("Missing mandatory information requestID for actionType:%q", action.ActionType)
	}

	var status string
	switch action.ActionType {
	case computeBestLocationAction:
		// TODO: call the business logic API to get the request status
		status = TaskStatusDoneMsg
	default:
		err = errors.Errorf("Unsupported action %s", action.ActionType)
	}
	if err != nil {
		return true, err
	}

	var requestStatus string
	var errorMessage string
	switch {
	case status == TaskStatusPendingMsg:
		requestStatus = requestStatusPending
	case status == TaskStatusInProgressMsg:
		requestStatus = requestStatusRunning
	case status == TaskStatusDoneMsg:
		requestStatus = requestStatusCompleted
	default:
		return true, errors.Errorf("Unexpected status :%q", status)
	}

	previousRequestStatus, err := deployments.GetInstanceStateString(ctx, deploymentID, actionData.nodeName, "0")
	if err != nil {
		previousRequestStatus = "initial"
	}

	// See if monitoring must be continued and set job state if terminated
	switch requestStatus {
	case requestStatusCompleted:
		// job has been done successfully : unregister monitoring
		deregister = true
		// Update locations
		err = o.setLocations(ctx, deploymentID, actionData.nodeName)
	case requestStatusPending, requestStatusRunning:
		// job's still running or its state is about to be set definitively: monitoring is keeping on (deregister stays false)
	default:
		// Other cases as FAILED, CANCELED : error is return with job state and job info is logged
		deregister = true
		// Log event containing all the slurm information

		events.WithContextOptionalFields(ctx).NewLogEntry(events.LogLevelERROR, deploymentID).RegisterAsString(fmt.Sprintf("request %s status: %s, reason: %s", requestID, requestStatus, errorMessage))
		// Error to be returned
		err = errors.Errorf("Request ID %s finished unsuccessfully with status: %s, reason: %s", requestID, requestStatus, errorMessage)
	}

	// Print state change
	if previousRequestStatus != requestStatus {
		errSet := deployments.SetInstanceStateStringWithContextualLogs(ctx, deploymentID, actionData.nodeName, "0", requestStatus)
		if errSet != nil {
			log.Printf("Failed to set instance %s %s state %s: %s", deploymentID, actionData.nodeName, requestStatus, errSet.Error())
		}
	}

	return deregister, err
}

func (o *ActionOperator) getActionData(action *prov.Action) (*actionData, error) {
	var ok bool
	actionData := &actionData{}
	// Check nodeName
	actionData.nodeName, ok = action.Data[actionDataNodeName]
	if !ok {
		return actionData, errors.Errorf("Missing mandatory information nodeName for actionType:%q", action.ActionType)
	}
	// Check taskID
	actionData.taskID, ok = action.Data[actionDataTaskID]
	if !ok {
		return actionData, errors.Errorf("Missing mandatory information taskID for actionType:%q", action.ActionType)
	}
	return actionData, nil
}

func (o *ActionOperator) setLocations(ctx context.Context, deploymentID, nodeName string) error {

	var err error

	cloudReqs, err := getStoredCloudRequirements(ctx, deploymentID, nodeName)
	if err != nil {
		return err
	}
	datasetReqs, err := getStoredDatasetRequirements(ctx, deploymentID, nodeName)
	if err != nil {
		return err
	}

	hpcReqs, err := getStoredHPCRequirements(ctx, deploymentID, nodeName)
	if err != nil {
		return err
	}

	// Compute locations fulfilling these requirements
	cloudLocations, hpcLocations, err := o.computeLocations(ctx, deploymentID, nodeName, cloudReqs, hpcReqs, datasetReqs)
	if err != nil {
		return err
	}

	// Assign locations to cloud instances
	err = o.assignCloudLocations(ctx, deploymentID, cloudReqs, cloudLocations)
	if err != nil {
		return err
	}

	// Assign locations to HEAppE jobs
	err = o.assignHPCLocations(ctx, deploymentID, hpcReqs, hpcLocations)
	return err
}

func (o *ActionOperator) computeLocations(ctx context.Context, deploymentID, nodeName string, cloudReqs map[string]CloudRequirement,
	hpcReqs map[string]HPCRequirement, datasetReqs map[string]DatasetRequirement) (map[string]CloudLocation, map[string]HPCLocation, error) {

	cloudLocations := make(map[string]CloudLocation)
	hpcLocations := make(map[string]HPCLocation)
	var err error

	// TODO: call dynamic allocation business logic
	// instead of using this test code
	datacenter := "lrz"
	for datasetName, datasetReq := range datasetReqs {
		locs := datasetReq.Locations
		if len(locs) > 0 {
			// Convention: the first section of location identify the datacenter
			datacenter = strings.ToLower(strings.SplitN(locs[0], "_", 2)[0])
			events.WithContextOptionalFields(ctx).NewLogEntry(events.LogLevelINFO, deploymentID).Registerf(
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
				events.WithContextOptionalFields(ctx).NewLogEntry(events.LogLevelINFO, deploymentID).Registerf(
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
				imageID = "08e1da22-77fe-491c-b366-a1dae9f31458"
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
				imageID = "3785b748-ebaf-4763-8c83-bb7d1759cb9c"
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

		cloudLocations[nodeName] = CloudLocation{
			Name:           datacenter,
			Flavor:         flavorName,
			ImageID:        imageID,
			FloatingIPPool: floatingIPPool,
			User:           user,
		}
	}

	// Store new collected requirements value
	err = deployments.SetAttributeComplexForAllInstances(ctx, deploymentID, nodeName,
		cloudLocationsConsulAttribute, cloudLocations)
	if err != nil {
		err = errors.Wrapf(err, "Failed to store cloud locations results for deployment %s node %s",
			deploymentID, nodeName)
		return cloudLocations, hpcLocations, err
	}

	// Store locations in an attribute exposed in Alien4Cloud
	nodesLocations := make(map[string]string)
	for n, val := range cloudLocations {
		nodesLocations[n] = val.Name + "_openstack"
	}

	for nodeName, jobSpec := range hpcReqs {
		location := "it4i_heappe"

		if strings.HasPrefix(strings.ToLower(jobSpec.Tasks[0].Name), "runtraf") ||
			strings.HasPrefix(strings.ToLower(jobSpec.Tasks[0].Name), "openfoam") {
			location = "it4i_heappe_wp5"
		}

		taskLocation := TaskLocation{
			NodeTypeID:        jobSpec.Tasks[0].ClusterNodeTypeID,
			CommandTemplateID: jobSpec.Tasks[0].CommandTemplateID,
		}
		tasksLocations := map[string]TaskLocation{
			jobSpec.Tasks[0].Name: taskLocation,
		}
		hpcLocations[nodeName] = HPCLocation{
			Name:          location,
			Project:       jobSpec.Project,
			TasksLocation: tasksLocations,
		}
	}

	// Store new collected requirements value
	err = deployments.SetAttributeComplexForAllInstances(ctx, deploymentID, nodeName,
		hpcLocationsConsulAttribute, hpcLocations)
	if err != nil {
		err = errors.Wrapf(err, "Failed to store cloud locations results for deployment %s node %s",
			deploymentID, nodeName)
		return cloudLocations, hpcLocations, err
	}

	// Store locations in an attribute exposed in Alien4Cloud
	for n, val := range hpcLocations {
		nodesLocations[n] = val.Name
	}
	v, err := json.Marshal(nodesLocations)
	if err != nil {
		return cloudLocations, hpcLocations, err
	}

	err = deployments.SetAttributeForAllInstances(ctx, deploymentID, nodeName,
		nodesLocationsConsulAttribute, string(v))

	return cloudLocations, hpcLocations, err

}

func (o *ActionOperator) assignCloudLocations(ctx context.Context, deploymentID string,
	requirements map[string]CloudRequirement, locations map[string]CloudLocation) error {

	var err error
	for nodeName, req := range requirements {
		location, ok := locations[nodeName]
		if !ok {
			if req.Optional {
				events.WithContextOptionalFields(ctx).NewLogEntry(events.LogLevelINFO, deploymentID).Registerf(
					"No available location for optional compute instance %s in deployment %s", nodeName, deploymentID)
				err = o.setCloudLocationSkipped(ctx, nodeName)
				if err != nil {
					return err
				}
			} else {
				return errors.Errorf("No available location found for compute instance %s in deployment %s", nodeName, deploymentID)
			}
		}
		location.Name = location.Name + "_openstack"
		events.WithContextOptionalFields(ctx).NewLogEntry(events.LogLevelINFO, deploymentID).Registerf(
			"Location for %s: %s", nodeName, location.Name)
		err = o.setCloudLocation(ctx, deploymentID, nodeName, req, location)

	}
	return err
}

func (o *ActionOperator) assignHPCLocations(ctx context.Context, deploymentID string, requirements map[string]HPCRequirement, locations map[string]HPCLocation) error {

	var err error
	for nodeName, req := range requirements {
		location, ok := locations[nodeName]
		if !ok {
			if req.Optional {
				events.WithContextOptionalFields(ctx).NewLogEntry(events.LogLevelINFO, deploymentID).Registerf(
					"No available location for optional compute instance %s in deployment %s", nodeName, deploymentID)
				err = o.setHPCLocationSkipped(ctx, nodeName)
				if err != nil {
					return err
				}
			} else {
				return errors.Errorf("No available location found for compute instance %s in deployment %s", nodeName, deploymentID)
			}
		}
		events.WithContextOptionalFields(ctx).NewLogEntry(events.LogLevelINFO, deploymentID).Registerf(
			"Location for %s: %s", nodeName, location.Name)

		err = o.setHPCLocation(ctx, deploymentID, nodeName, req, location)

	}
	return err
}

// setCloudLocation updates the deployment description of a compute instance for a new location
func (o *ActionOperator) setCloudLocation(ctx context.Context, deploymentID, nodeName string, requirement CloudRequirement, location CloudLocation) error {

	nodeTemplate, err := getStoredNodeTemplate(ctx, deploymentID, nodeName)
	if err != nil {
		return err
	}

	// Add the new location in this node template metadata
	if nodeTemplate.Metadata == nil {
		nodeTemplate.Metadata = make(map[string]string)
	}
	nodeTemplate.Metadata[tosca.MetadataLocationNameKey] = location.Name
	// Update the flavor
	flavorVal := tosca.ValueAssignment{
		Type:  tosca.ValueAssignmentLiteral,
		Value: location.Flavor,
	}
	nodeTemplate.Properties["flavorName"] = &flavorVal

	// Update to boot volume image ID
	val, ok := nodeTemplate.Properties["boot_volume"]
	if !ok {
		return errors.Errorf("Found no boot volume defined for node %s in deployment %s", nodeName, deploymentID)
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
		return errors.Errorf("Found no credentials defined for node %s in deployment %s", nodeName, deploymentID)
	}
	creds := val.GetMap()
	creds["user"] = location.User
	credsVal := tosca.ValueAssignment{
		Type:  tosca.ValueAssignmentMap,
		Value: creds,
	}

	nodeTemplate.Capabilities["endpoint"].Properties["credentials"] = &credsVal

	// Location is now changed for this node template, storing it
	err = storeNodeTemplate(ctx, deploymentID, nodeName, nodeTemplate)
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
		events.WithContextOptionalFields(ctx).NewLogEntry(events.LogLevelINFO, deploymentID).Registerf(
			"No floating IP associated to compute instance %s in deployment %s", nodeName, deploymentID)
		return err
	}

	fipNodeTemplate, err := getStoredNodeTemplate(ctx, deploymentID, floatingIPNodeName)
	if err != nil {
		return err
	}
	if fipNodeTemplate.Metadata == nil {
		fipNodeTemplate.Metadata = make(map[string]string)
	}
	fipNodeTemplate.Metadata[tosca.MetadataLocationNameKey] = location.Name

	// Update as well the Floating IP pool
	poolVal := tosca.ValueAssignment{
		Type:  tosca.ValueAssignmentLiteral,
		Value: location.FloatingIPPool,
	}
	fipNodeTemplate.Properties["floating_network_name"] = &poolVal

	// Location is now changed for this node template, storing it
	err = storeNodeTemplate(ctx, deploymentID, floatingIPNodeName, fipNodeTemplate)
	if err != nil {
		return err
	}

	events.WithContextOptionalFields(ctx).NewLogEntry(events.LogLevelINFO, deploymentID).Registerf(
		"Floating IP pool is %s for %s in deployment %s", location.FloatingIPPool, floatingIPNodeName, deploymentID)

	return err
}

// setCloudLocationSkipped updates the deployment description of a compute instance that has to be skipped
func (o *ActionOperator) setCloudLocationSkipped(ctx context.Context, nodeName string) error {
	return errors.Errorf("Skipping a cloud compute instance without location not yet implemented")
}

// setHPCLocation updates the deployment description of a HPC job for a new location
func (o *ActionOperator) setHPCLocation(ctx context.Context, deploymentID, nodeName string,
	requirement HPCRequirement, location HPCLocation) error {

	nodeTemplate, err := getStoredNodeTemplate(ctx, deploymentID, nodeName)
	if err != nil {
		return err
	}

	// Add the new location in this node template metadata
	if nodeTemplate.Metadata == nil {
		nodeTemplate.Metadata = make(map[string]string)
	}
	nodeTemplate.Metadata[tosca.MetadataLocationNameKey] = location.Name

	// Update the job specification
	jobSpecVal, ok := nodeTemplate.Properties["JobSpecification"]
	if !ok {
		return errors.Errorf("Found no property JobSpecification in Node Template %+v", nodeTemplate)
	}
	jobSpecMap := jobSpecVal.GetMap()
	jobSpecMap["Project"] = location.Project

	// Update the tasks
	tasksVal, ok := jobSpecMap["Tasks"]
	if !ok {
		return errors.Errorf("Found no property Tasks in Node Template %+v", nodeTemplate)
	}
	tasksArray, _ := tasksVal.([]interface{})
	for taskName, taskLocation := range location.TasksLocation {
		for _, task := range tasksArray {
			tMap, _ := task.(map[string]interface{})
			if taskName == tMap["Name"] {
				tMap["ClusterNodeTypeId"] = taskLocation.NodeTypeID
				tMap["CommandTemplateId"] = taskLocation.CommandTemplateID
				break
			}
		}
	}

	jobSpecMap["Tasks"] = tasksArray

	jobSpecVal, err = tosca.ToValueAssignment(jobSpecMap)
	if err != nil {
		return errors.Wrapf(err, "Failed to translate map to value assignment: %+v", jobSpecMap)
	}
	nodeTemplate.Properties["JobSpecification"] = jobSpecVal

	// Location is now changed for this node template, storing it
	err = storeNodeTemplate(ctx, deploymentID, nodeName, nodeTemplate)
	if err != nil {
		return err
	}

	return err
}

// setHPCLocationSkipped updates the deployment description of a compute instance that has to be skipped
func (o *ActionOperator) setHPCLocationSkipped(ctx context.Context, nodeName string) error {
	return errors.Errorf("Skipping a HPC job without location not yet implemented")
}

// getStoredNodeTemplate returns the description of a node stored by Yorc
func getStoredNodeTemplate(ctx context.Context, deploymentID, nodeName string) (*tosca.NodeTemplate, error) {
	node := new(tosca.NodeTemplate)
	nodePath := path.Join(consulutil.DeploymentKVPrefix, deploymentID, "topology", "nodes", nodeName)
	found, err := storage.GetStore(storageTypes.StoreTypeDeployment).Get(nodePath, node)
	if !found {
		err = errors.Errorf("No such node %s in deployment %s", nodeName, deploymentID)
	}
	return node, err
}

// storeNodeTemplate stores a node template in Yorc
func storeNodeTemplate(ctx context.Context, deploymentID, nodeName string, nodeTemplate *tosca.NodeTemplate) error {
	nodePrefix := path.Join(consulutil.DeploymentKVPrefix, deploymentID, "topology", "nodes", nodeName)
	return storage.GetStore(storageTypes.StoreTypeDeployment).Set(ctx, nodePrefix, nodeTemplate)
}
