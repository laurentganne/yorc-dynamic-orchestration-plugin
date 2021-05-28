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
	"fmt"
	"strings"
	"time"

	"github.com/hashicorp/consul/api"
	"github.com/lexis-project/yorcoidc"
	"github.com/pkg/errors"

	"github.com/ystia/yorc/v4/config"
	"github.com/ystia/yorc/v4/events"
	"github.com/ystia/yorc/v4/locations"
	"github.com/ystia/yorc/v4/prov"
	"github.com/ystia/yorc/v4/prov/operations"
	"github.com/ystia/yorc/v4/tosca"
)

// ValidateExchangeToken holds properties of a component validating a token
// and exhanging it to get access/refresh tokens for the orchestrator client
type ValidateExchangeToken struct {
	KV             *api.KV
	Cfg            config.Configuration
	AAIClient      yorcoidc.Client
	DeploymentID   string
	TaskID         string
	NodeName       string
	Operation      prov.Operation
	Token          string
	EnvInputs      []*operations.EnvInput
	VarInputsNames []string
}

// ExecuteAsync is not supported here
func (v *ValidateExchangeToken) ExecuteAsync(ctx context.Context) (*prov.Action, time.Duration, error) {
	return nil, 0, errors.Errorf("Unsupported asynchronous operation %s", v.Operation.Name)
}

// Execute executes a synchronous operation
func (v *ValidateExchangeToken) Execute(ctx context.Context) error {

	var err error
	switch strings.ToLower(v.Operation.Name) {
	case "install", "standard.create":
		events.WithContextOptionalFields(ctx).NewLogEntry(events.LogLevelINFO, v.DeploymentID).Registerf(
			"Installing %q", v.NodeName)
		// Nothing to do here
	case "standard.start":
		events.WithContextOptionalFields(ctx).NewLogEntry(events.LogLevelINFO, v.DeploymentID).Registerf(
			"Creating %q", v.NodeName)
		err = v.validateAndExchangeToken(ctx)
		if err != nil {
			return err
		}
	case "uninstall", "standard.delete":
		events.WithContextOptionalFields(ctx).NewLogEntry(events.LogLevelINFO, v.DeploymentID).Registerf(
			"Deleting %q", v.NodeName)
		// Nothing to do here
	case "standard.stop":
		// Nothing to do
		events.WithContextOptionalFields(ctx).NewLogEntry(events.LogLevelINFO, v.DeploymentID).Registerf(
			"Executing operation %s on node %q", v.Operation.Name, v.NodeName)
	case tosca.RunnableSubmitOperationName, tosca.RunnableCancelOperationName:
		err = errors.Errorf("Unsupported operation %s", v.Operation.Name)
	default:
		err = errors.Errorf("Unsupported operation %s", v.Operation.Name)
	}

	return err
}

// ResolveExecution resolves inputs before the execution of an operation
func (v *ValidateExchangeToken) ResolveExecution(ctx context.Context) error {
	return v.resolveInputs(ctx)
}

func (v *ValidateExchangeToken) resolveInputs(ctx context.Context) error {
	var err error
	v.EnvInputs, v.VarInputsNames, err = operations.ResolveInputsWithInstances(
		ctx, v.DeploymentID, v.NodeName, v.TaskID, v.Operation, nil, nil)
	return err
}

func (v *ValidateExchangeToken) validateAndExchangeToken(ctx context.Context) error {
	locationMgr, err := locations.GetManager(v.Cfg)
	if err != nil {
		return err
	}
	locationProps, err := locationMgr.GetLocationPropertiesForNode(ctx,
		v.DeploymentID, v.NodeName, ddiInfrastructureType)
	if err == nil && len(locationProps) == 0 {
		locationProps, err = locationMgr.GetLocationPropertiesForNode(ctx,
			v.DeploymentID, v.NodeName, heappeInfrastructureType)
	}
	if err != nil {
		return err
	}

	// Getting an AAI client to check token validity
	aaiClient := getAAIClient(v.DeploymentID, locationProps)
	valid, err := aaiClient.IsAccessTokenValid(ctx, v.Token)
	if err != nil {
		return errors.Wrapf(err, "Failed to check validity of token")
	}

	if !valid {
		errorMsg := fmt.Sprintf("Token provided in input for node %s is not anymore valid", v.NodeName)
		events.WithContextOptionalFields(ctx).NewLogEntry(events.LogLevelERROR, v.DeploymentID).Registerf(errorMsg)
		return errors.Errorf(errorMsg)
	}

	// Exchange this token to get one access/refresh token for the orchestrator client
	_, _, err = aaiClient.ExchangeToken(ctx, v.Token)

	return err
}
