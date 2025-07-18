/*
Copyright 2023 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package input

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"strings"
	"time"

	promapi "github.com/prometheus/client_golang/api"
	prometheusv1 "github.com/prometheus/client_golang/api/prometheus/v1"
	prommodel "github.com/prometheus/common/model"
	"k8s.io/klog/v2"

	"k8s.io/autoscaler/vertical-pod-autoscaler/pkg/recommender/input/history"
	"k8s.io/autoscaler/vertical-pod-autoscaler/pkg/recommender/model"
	metrics_recommender "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/utils/metrics/recommender"
)

// PodMemoryResult represents a memory value for a specific pod
type PodMemoryResult struct {
	PodID       model.PodID
	MemoryBytes model.ResourceAmount
}

// PromQLClient handles executing PromQL queries during real-time metrics collection
type PromQLClient interface {
	// ExecuteQueries executes the configured PromQL queries and returns memory values per pod
	ExecuteQueries(ctx context.Context) ([]PodMemoryResult, error)
}

// promqlClient implements PromQLClient
type promqlClient struct {
	prometheusClient prometheusv1.API
	queries          []string
	queryTimeout     time.Duration
}

// NewPromQLClient creates a new PromQL client with the given configuration
func NewPromQLClient(config history.PrometheusHistoryProviderConfig, queries []string) (PromQLClient, error) {
	if len(queries) == 0 {
		return &noOpPromQLClient{}, nil
	}

	// Reuse the same transport setup as history provider
	prometheusTransport := promapi.DefaultRoundTripper

	if config.Insecure {
		prometheusTransport.(*http.Transport).TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	}

	if config.Authentication.BearerToken != "" {
		prometheusTransport = &history.PrometheusBearerTokenAuthTransport{
			Token: config.Authentication.BearerToken,
			Base:  prometheusTransport,
		}
	} else if config.Authentication.Username != "" && config.Authentication.Password != "" {
		prometheusTransport = &history.PrometheusBasicAuthTransport{
			Username: config.Authentication.Username,
			Password: config.Authentication.Password,
			Base:     prometheusTransport,
		}
	}

	roundTripper := metrics_recommender.NewPrometheusRoundTripperCounter(
		metrics_recommender.NewPrometheusRoundTripperDuration(prometheusTransport),
	)

	promConfig := promapi.Config{
		Address:      config.Address,
		RoundTripper: roundTripper,
	}

	promClient, err := promapi.NewClient(promConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create Prometheus client: %v", err)
	}

	return &promqlClient{
		prometheusClient: prometheusv1.NewAPI(promClient),
		queries:          queries,
		queryTimeout:     config.QueryTimeout,
	}, nil
}

// ParsePromQLQueries parses a semicolon-separated string of PromQL queries
func ParsePromQLQueries(queryString string) []string {
	if queryString == "" {
		return nil
	}

	queries := strings.Split(queryString, ";")
	var result []string
	for _, query := range queries {
		query = strings.TrimSpace(query)
		if query != "" {
			result = append(result, query)
		}
	}
	return result
}

// ExecuteQueries executes all configured PromQL queries and returns the maximum memory value per pod
func (c *promqlClient) ExecuteQueries(ctx context.Context) ([]PodMemoryResult, error) {
	// Map to track maximum memory value per pod across all queries
	podMaxMemory := make(map[model.PodID]model.ResourceAmount)

	queryCtx, cancel := context.WithTimeout(ctx, c.queryTimeout)
	defer cancel()

	for i, query := range c.queries {
		klog.V(4).InfoS("Executing PromQL query", "query", query, "index", i)

		result, _, err := c.prometheusClient.Query(queryCtx, query, time.Now())
		if err != nil {
			klog.ErrorS(err, "Failed to execute PromQL query", "query", query)
			continue // Continue with other queries even if one fails
		}

		podResults, err := c.extractPodMemoryValues(result)
		if err != nil {
			klog.ErrorS(err, "Failed to extract pod memory values from PromQL result", "query", query)
			continue
		}

		// Update maximum memory values per pod
		for _, podResult := range podResults {
			if existingMax, exists := podMaxMemory[podResult.PodID]; exists {
				if podResult.MemoryBytes > existingMax {
					podMaxMemory[podResult.PodID] = podResult.MemoryBytes
				}
			} else {
				podMaxMemory[podResult.PodID] = podResult.MemoryBytes
			}
		}

		klog.V(4).InfoS("PromQL query result", "query", query, "podCount", len(podResults))
	}

	// Convert map to slice of results with maximum values only
	var results []PodMemoryResult
	for podID, maxMemory := range podMaxMemory {
		results = append(results, PodMemoryResult{
			PodID:       podID,
			MemoryBytes: maxMemory,
		})
	}

	klog.V(3).InfoS("PromQL queries completed", "totalQueries", len(c.queries), "uniquePods", len(results))
	return results, nil
}

// extractPodMemoryValues extracts memory values per pod from a Prometheus query result
func (c *promqlClient) extractPodMemoryValues(result prommodel.Value) ([]PodMemoryResult, error) {
	switch v := result.(type) {
	case prommodel.Vector:
		var results []PodMemoryResult

		for _, sample := range v {
			// Extract memory value
			if sample.Value.String() == "NaN" || sample.Value.String() == "+Inf" || sample.Value.String() == "-Inf" {
				continue // Skip invalid values
			}
			bytes := float64(sample.Value)
			if bytes < 0 {
				bytes = 0 // Convert negative values to 0
			}
			memoryAmount := model.MemoryAmountFromBytes(bytes)

			// Extract pod and namespace labels
			podID, err := c.extractPodID(sample.Metric)
			if err != nil {
				klog.V(4).InfoS("Skipping sample without valid pod labels", "metric", sample.Metric, "error", err)
				continue
			}

			results = append(results, PodMemoryResult{
				PodID:       *podID,
				MemoryBytes: memoryAmount,
			})
		}

		return results, nil

	default:
		return nil, fmt.Errorf("unsupported result type: %T", result)
	}
}

// extractPodID extracts pod ID from metric labels
func (c *promqlClient) extractPodID(metric prommodel.Metric) (*model.PodID, error) {
	// Look for common label names for pod and namespace
	podLabelNames := []string{"pod", "pod_name", "kubernetes_pod_name"}
	namespaceLabelNames := []string{"namespace", "kubernetes_namespace"}

	var podName, namespace string

	// Find pod name
	for _, labelName := range podLabelNames {
		if value, exists := metric[prommodel.LabelName(labelName)]; exists && string(value) != "" {
			podName = string(value)
			break
		}
	}

	// Find namespace
	for _, labelName := range namespaceLabelNames {
		if value, exists := metric[prommodel.LabelName(labelName)]; exists && string(value) != "" {
			namespace = string(value)
			break
		}
	}

	if podName == "" {
		return nil, fmt.Errorf("pod name not found in metric labels")
	}
	if namespace == "" {
		return nil, fmt.Errorf("namespace not found in metric labels")
	}

	return &model.PodID{
		Namespace: namespace,
		PodName:   podName,
	}, nil
}

// noOpPromQLClient is used when no PromQL queries are configured
type noOpPromQLClient struct{}

func (c *noOpPromQLClient) ExecuteQueries(ctx context.Context) ([]PodMemoryResult, error) {
	return nil, nil
}
