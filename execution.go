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
	"time"

	"github.com/laurentganne/yorcoidc"
	"github.com/pkg/errors"

	"github.com/ystia/yorc/v4/config"
	"github.com/ystia/yorc/v4/deployments"
	"github.com/ystia/yorc/v4/events"
	"github.com/ystia/yorc/v4/locations"
	"github.com/ystia/yorc/v4/prov"
)

const (
	ddiInfrastructureType                 = "ddi"
	heappeInfrastructureType              = "heappe"
	locationDefaultMonitoringTimeInterval = 5 * time.Second
	locationJobMonitoringTimeInterval     = "job_monitoring_time_interval"
	setLocationsComponentType             = "org.lexis.common.dynamic.orchestration.nodes.SetLocationsJob"
	validateAndExchangeTokenComponentType = "org.lexis.common.dynamic.orchestration.nodes.ValidateAndExchangeToken"
	locationAAIURL                        = "aai_url"
	locationAAIClientID                   = "aai_client_id"
	locationAAIClientSecret               = "aai_client_secret"
	locationAAIRealm                      = "aai_realm"
)

// Execution is the interface holding functions to execute an operation
type Execution interface {
	ResolveExecution(ctx context.Context) error
	ExecuteAsync(ctx context.Context) (*prov.Action, time.Duration, error)
	Execute(ctx context.Context) error
}

func newExecution(ctx context.Context, cfg config.Configuration, taskID, deploymentID, nodeName string,
	operation prov.Operation) (Execution, error) {

	consulClient, err := cfg.GetConsulClient()
	if err != nil {
		return nil, err
	}
	kv := consulClient.KV()

	var exec Execution

	isValidateToken, err := deployments.IsNodeDerivedFrom(ctx, deploymentID, nodeName, validateAndExchangeTokenComponentType)
	if err != nil {
		return exec, err
	}

	if isValidateToken {
		token, err := deployments.GetStringNodePropertyValue(ctx, deploymentID, nodeName, "token")
		if err != nil {
			return exec, err
		}
		if token == "" {
			return exec, errors.Errorf("No value provided for deployement %s node %s property token", deploymentID, nodeName)
		}
		exec = &ValidateExchangeToken{
			KV:           kv,
			Cfg:          cfg,
			DeploymentID: deploymentID,
			TaskID:       taskID,
			NodeName:     nodeName,
			Operation:    operation,
			Token:        token,
		}
		return exec, exec.ResolveExecution(ctx)

	}

	locationMgr, err := locations.GetManager(cfg)
	if err != nil {
		return nil, err
	}
	locationProps, err := locationMgr.GetLocationPropertiesForNode(ctx,
		deploymentID, nodeName, ddiInfrastructureType)
	if err == nil && len(locationProps) == 0 {
		locationProps, err = locationMgr.GetLocationPropertiesForNode(ctx,
			deploymentID, nodeName, heappeInfrastructureType)
	}
	if err != nil {
		return exec, err
	}

	// Getting an AAI client to check token validity
	aaiClient := getAAIClient(deploymentID, locationProps)

	accessToken, err := aaiClient.GetAccessToken()
	if err != nil {
		return nil, err
	}

	if accessToken == "" {
		token, err := deployments.GetStringNodePropertyValue(ctx, deploymentID,
			nodeName, "token")
		if err != nil {
			return exec, err
		}

		if token == "" {
			return exec, errors.Errorf("Found no token node %s in deployment %s", nodeName, deploymentID)
		}

		valid, err := aaiClient.IsAccessTokenValid(ctx, token)
		if err != nil {
			return exec, errors.Wrapf(err, "Failed to check validity of token")
		}

		if !valid {
			errorMsg := fmt.Sprintf("Token provided in input for Job %s is not anymore valid", nodeName)
			events.WithContextOptionalFields(ctx).NewLogEntry(events.LogLevelERROR, deploymentID).Registerf(errorMsg)
			return exec, errors.Errorf(errorMsg)
		}
		// Exchange this token for an access and a refresh token for the orchestrator
		accessToken, _, err = aaiClient.ExchangeToken(ctx, token)
		if err != nil {
			return exec, errors.Wrapf(err, "Failed to exchange token for orchestrator")
		}

	}

	// Checking the access token validity
	valid, err := aaiClient.IsAccessTokenValid(ctx, accessToken)
	if err != nil {
		return exec, errors.Wrapf(err, "Failed to check validity of access token")
	}

	if !valid {
		_, _, err = aaiClient.RefreshToken(ctx)
		if err != nil {
			return exec, errors.Wrapf(err, "Failed to refresh token for orchestrator")
		}
	}

	monitoringTimeInterval := locationProps.GetDuration(locationJobMonitoringTimeInterval)
	if monitoringTimeInterval <= 0 {
		// Default value
		monitoringTimeInterval = locationDefaultMonitoringTimeInterval
	}

	isSetLocationsComponent, err := deployments.IsNodeDerivedFrom(ctx, deploymentID, nodeName, setLocationsComponentType)
	if err != nil {
		return exec, err
	}

	if isSetLocationsComponent {
		exec = &SetLocationsExecution{
			KV:                     kv,
			Cfg:                    cfg,
			DeploymentID:           deploymentID,
			TaskID:                 taskID,
			NodeName:               nodeName,
			Operation:              operation,
			MonitoringTimeInterval: monitoringTimeInterval,
		}
		return exec, exec.ResolveExecution(ctx)
	}

	return exec, errors.Errorf("operation %q supported only for nodes derived from %q",
		operation, setLocationsComponentType)
}

// getAAIClient returns the AAI client for a given location
func getAAIClient(deploymentID string, locationProps config.DynamicMap) yorcoidc.Client {
	url := locationProps.GetString(locationAAIURL)
	clientID := locationProps.GetString(locationAAIClientID)
	clientSecret := locationProps.GetString(locationAAIClientSecret)
	realm := locationProps.GetString(locationAAIRealm)
	return yorcoidc.GetClient(deploymentID, url, clientID, clientSecret, realm)
}
