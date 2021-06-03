// Copyright 2020 Bull S.A.S. Atos Technologies - Bull, Rue Jean Jaures, B.P.68, 78340, Les Clayes-sous-Bois, France.
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

// Dynamic Allocator Module (DAM) interface

package dam

import (
	"net/http"
	"path"

	"github.com/pkg/errors"

	"github.com/ystia/yorc/v4/config"
)

const (
	// RequestStatusOK is the stauts of a successful request
	RequestStatusOK = "ok"

	invalidTokenError = "Invalid Token"

	evaluateCloudEndpoint  = "/evaluate/machines?type=cloud"
	evaluateHPCEndpoint    = "/evaluate/machines?type=hpc"
	evaluateStatusEndpoint = "/get/machines"

	locationDynamicAllocatorModuleURLPropertyName = "url"
)

// RefreshTokenFunc is a type of function provided by the caller to refresh a token when needed
type RefreshTokenFunc func() (newAccessToken string, err error)

// Client is the client interface to the Dynamic Allocator Module (DAM) service
type Client interface {
	// SubmitCloudPlacementRequest submits a request to find the best placement for cloud compute instances
	SubmitCloudPlacementRequest(token string, requirement CloudRequirement) (SubmittedRequestInfo, error)
	// SubmitHPCPlacementRequest submits a request to find the best placement for HPC HEAppE jobs
	SubmitHPCPlacementRequest(token string, requirement HPCRequirement) (SubmittedRequestInfo, error)
	// GetCloudPlacementRequestStatus returns the status of a request to find the best cloud placement
	GetCloudPlacementRequestStatus(token string, requestID string) (CloudPlacement, error)
	// GetHPCPlacementRequestStatus returns the status of a request to find the best HPC placement
	GetHPCPlacementRequestStatus(token string, requestID string) (HPCPlacement, error)
}

// GetClient returns a DDI client for a given location
func GetClient(locationProps config.DynamicMap, refreshTokenFunc RefreshTokenFunc) (Client, error) {

	url := locationProps.GetString(locationDynamicAllocatorModuleURLPropertyName)
	if url == "" {
		return nil, errors.Errorf("No %s property defined in Dynamic Allocator Module location configuration", locationDynamicAllocatorModuleURLPropertyName)
	}

	return &damClient{
		URL:        url,
		httpClient: getHTTPClient(url, refreshTokenFunc),
	}, nil
}

type damClient struct {
	httpClient *httpclient
	URL        string
}

// SubmitCloudPlacementRequest submits a request to find the best placement for cloud compute instances
func (b *damClient) SubmitCloudPlacementRequest(token string, requirement CloudRequirement) (SubmittedRequestInfo, error) {

	var response SubmittedRequestInfo
	err := b.httpClient.doRequest(http.MethodPost, evaluateCloudEndpoint,
		[]int{http.StatusOK, http.StatusCreated, http.StatusAccepted}, token, requirement, &response)
	if err != nil {
		err = errors.Wrapf(err, "Failed to submit cloud placement request %v",
			requirement)
	}

	return response, err
}

// SubmitHPCPlacementRequest submits a request to find the best placement for HPC HEAppE job
func (b *damClient) SubmitHPCPlacementRequest(token string, requirement HPCRequirement) (SubmittedRequestInfo, error) {

	var response SubmittedRequestInfo
	err := b.httpClient.doRequest(http.MethodPost, evaluateHPCEndpoint,
		[]int{http.StatusOK, http.StatusCreated, http.StatusAccepted}, token, requirement, &response)
	if err != nil {
		err = errors.Wrapf(err, "Failed to submit HPC placement request %v",
			requirement)
	}

	return response, err
}

// GetCloudPlacementRequestStatus returns the status of a request to find the best cloud placement
func (b *damClient) GetCloudPlacementRequestStatus(token string, requestID string) (CloudPlacement, error) {

	var response CloudPlacement
	err := b.httpClient.doRequest(http.MethodGet, path.Join(evaluateStatusEndpoint, requestID),
		[]int{http.StatusOK}, token, nil, &response)
	if err != nil {
		err = errors.Wrapf(err, "Failed to submit get cloud placement status for request %s", requestID)
	}

	return response, err
}

// GetHPCPlacementRequestStatus returns the status of a request to find the best HPC placement
func (b *damClient) GetHPCPlacementRequestStatus(token string, requestID string) (HPCPlacement, error) {

	var response HPCPlacement
	err := b.httpClient.doRequest(http.MethodGet, path.Join(evaluateStatusEndpoint, requestID),
		[]int{http.StatusOK}, token, nil, &response)
	if err != nil {
		err = errors.Wrapf(err, "Failed to submit get HPC placement status for request %s", requestID)
	}

	return response, err
}
