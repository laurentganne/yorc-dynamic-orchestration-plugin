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

// Dynamic Allocator Module (DAM) structures
package dam

// StorageInput holds properties of storage inputs that will be used be transferred
// to the Cloud instance or HEApPE Job
type StorageInput struct {
	Size          int      `json:"size"`
	Locations     []string `json:"locations"`
	NumberOfFiles int      `json:"numberOfFiles"`
}

// CloudRequirement holds parameters of requirements for Cloud instances to allocate
type CloudRequirement struct {
	NumberOfLocations int            `json:"number"`
	NumberOfInstances int            `json:"inst"`
	Project           string         `json:"project"`
	OSVersion         string         `json:"os_version"`
	MaxWallTime       int            `json:"max_walltime"`
	CPUs              int            `json:"vCPU"`
	Memory            int            `json:"mem"`
	Disk              int            `json:"disk"`
	StorageInputs     []StorageInput `json:"storage_inputs"`
}

// HPCRequirement holds parameters of requirements for HPC jobs to create
type HPCRequirement struct {
	Number        int            `json:"number"`
	Project       string         `json:"project"`
	MaxWallTime   int            `json:"max_walltime"`
	MaxCores      int            `json:"max_cores"`
	TaskName      string         `json:"taskName"`
	StorageInputs []StorageInput `json:"storage_inputs"`
}

// SubmittedRequestInfo holds the result of a request submission
type SubmittedRequestInfo struct {
	Message   string `json:"message,omitempty"`
	Status    string `json:"status"`
	RequestID string `json:"uid"`
}

// CloudLocation holds properties of a selected Cloud location
type CloudLocation struct {
	Flavor                string   `json:"flavour"`
	ImageID               string   `json:"image_id"`
	FloatingIPPool        string   `json:"NetworkIP"`
	Location              string   `json:"location"`
	StorageInputLocations []string `json:"storage_inputs"`
}

// TaskLocation holds properties of a selected HPC infrastructure for a HEAppE task
type TaskLocation struct {
	ClusterNodeTypeID int    `json:"cluster_node_type_id"`
	CommandTemplateID int    `json:"command_template_id"`
	TaskName          string `json:"name"`
}

// HPCLocation holds properties of a selected HPC location
type HPCLocation struct {
	URL                   string         `json:"HEAppE_URL"`
	ClusterID             int            `json:"cluster_id"`
	Location              string         `json:"location"`
	Project               string         `json:"project"`
	StorageInputLocations []string       `json:"storage_inputs"`
	TaskLocations         []TaskLocation `json:"tasks"`
}

type CloudPlacement struct {
	Status  string          `json:"status"`
	Message []CloudLocation `json:"message"`
}

type HPCPlacement struct {
	Status  string        `json:"status"`
	Message []HPCLocation `json:"message"`
}
