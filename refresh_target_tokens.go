// Copyright 2020 Bull r.A.S. Atos Technologies - Bull, Rue Jean Jaures, B.P.68, 78340, Les Clayes-sous-Bois, France.
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
	"strings"
	"time"

	"github.com/hashicorp/consul/api"
	"github.com/pkg/errors"

	"github.com/ystia/yorc/v4/config"
	"github.com/ystia/yorc/v4/deployments"
	"github.com/ystia/yorc/v4/events"
	"github.com/ystia/yorc/v4/locations"
	"github.com/ystia/yorc/v4/log"
	"github.com/ystia/yorc/v4/prov"
	"github.com/ystia/yorc/v4/tosca"
)

const targetRequirement = "target"

// RefreshTargetTokens holds propertied of a component refreshing tokens of a target
type RefreshTargetTokens struct {
	KV           *api.KV
	Cfg          config.Configuration
	DeploymentID string
	NodeName     string
	TaskID       string
	Operation    prov.Operation
}

// ExecuteAsync is not supported here
func (r *RefreshTargetTokens) ExecuteAsync(ctx context.Context) (*prov.Action, time.Duration, error) {
	return nil, 0, errors.Errorf("Unsupported asynchronous operation %s", r.Operation.Name)
}

// Execute executes a synchronous operation
func (r *RefreshTargetTokens) Execute(ctx context.Context) error {

	var err error
	switch strings.ToLower(r.Operation.Name) {
	case "standard.start":
		var locationProps config.DynamicMap
		var targetNodeName string
		locationProps, targetNodeName, err = r.getLocationTargetFromRequirement(ctx, targetRequirement)
		if err != nil {
			err = errors.Wrapf(err, "Failed to get target associated to %s", r.NodeName)
		} else {
			events.WithContextOptionalFields(ctx).NewLogEntry(events.LogLevelINFO, r.DeploymentID).Registerf(
				"Refreshing access token for target of %s: %s", r.NodeName, targetNodeName)
			log.Printf("RefreshTargetTokens component asks to refresh token for deployment %s\n", r.DeploymentID)
			aaiClient := getAAIClient(r.DeploymentID, locationProps)
			var accessToken string
			accessToken, _, err = aaiClient.RefreshToken(ctx)
			if err != nil {
				return err
			}
			err = deployments.SetAttributeForAllInstances(ctx, r.DeploymentID, targetNodeName,
				"access_token", accessToken)
		}
	case "install", "uninstall", "standard.create", "standard.stop", "standard.delete":
		// Nothing to do here
	case tosca.RunnableSubmitOperationName, tosca.RunnableCancelOperationName:
		err = errors.Errorf("Unsupported operation %s", r.Operation.Name)
	default:
		err = errors.Errorf("Unsupported operation %s", r.Operation.Name)
	}

	return err
}

// ResolveExecution resolves inputs before the execution of an operation
func (v *RefreshTargetTokens) ResolveExecution(ctx context.Context) error {
	return nil
}

func (r *RefreshTargetTokens) getLocationTargetFromRequirement(ctx context.Context, requirementName string) (config.DynamicMap, string, error) {
	// First get the associated compute node
	targetNodeName, err := deployments.GetTargetNodeForRequirementByName(ctx,
		r.DeploymentID, r.NodeName, requirementName)
	if err != nil {
		return nil, targetNodeName, err
	}

	locationMgr, err := locations.GetManager(r.Cfg)
	if err != nil {
		return nil, targetNodeName, err
	}

	locationProps, err := locationMgr.GetLocationPropertiesForNode(ctx,
		r.DeploymentID, targetNodeName, ddiInfrastructureType)
	if err == nil && len(locationProps) == 0 {
		locationProps, err = locationMgr.GetLocationPropertiesForNode(ctx,
			r.DeploymentID, targetNodeName, heappeInfrastructureType)
	}

	return locationProps, targetNodeName, err
}
