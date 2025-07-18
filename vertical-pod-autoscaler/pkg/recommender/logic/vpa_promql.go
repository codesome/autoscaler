package logic

import (
	"context"
	"fmt"
	"strings"
	"time"

	promapi "github.com/prometheus/client_golang/api"
	promv1 "github.com/prometheus/client_golang/api/v1"
	prommodel "github.com/prometheus/common/model"
	"k8s.io/klog/v2"

	"k8s.io/autoscaler/vertical-pod-autoscaler/pkg/recommender/input/history"
	"k8s.io/autoscaler/vertical-pod-autoscaler/pkg/recommender/model"
)

const (
	// VPA annotation keys for PromQL queries
	VPAPromQLQueriesAnnotation = "vpa.k8s.io/promql-memory-queries"
)

// VPAPromQLExecutor handles execution of PromQL queries from VPA annotations
type VPAPromQLExecutor struct {
	promClient promv1.API
}

// NewVPAPromQLExecutor creates a new VPA PromQL executor
func NewVPAPromQLExecutor(prometheusConfig history.PrometheusHistoryProviderConfig) (*VPAPromQLExecutor, error) {
	client, err := promapi.NewClient(promapi.Config{
		Address: prometheusConfig.Address,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create Prometheus client: %w", err)
	}

	return &VPAPromQLExecutor{
		promClient: promv1.NewAPI(client),
	}, nil
}

// ContainerMemoryResult holds memory recommendation result for a container
type ContainerMemoryResult struct {
	ContainerName string
	MemoryBytes   model.ResourceAmount
}

// ExecuteVPAPromQLQueries executes PromQL queries from VPA annotations and returns container memory recommendations
func (e *VPAPromQLExecutor) ExecuteVPAPromQLQueries(ctx context.Context, vpa *model.Vpa) ([]ContainerMemoryResult, error) {
	if vpa.Annotations == nil {
		return nil, nil
	}

	queriesAnnotation, exists := vpa.Annotations[VPAPromQLQueriesAnnotation]
	if !exists || queriesAnnotation == "" {
		return nil, nil
	}

	// Parse semicolon-separated queries
	queries := parsePromQLQueries(queriesAnnotation)
	if len(queries) == 0 {
		return nil, nil
	}

	klog.V(2).Infof("Executing %d PromQL queries for VPA %s/%s", len(queries), vpa.ID.Namespace, vpa.ID.VpaName)

	// Track maximum memory per container across all queries
	containerMaxMemory := make(map[string]model.ResourceAmount)

	for i, query := range queries {
		klog.V(3).Infof("Executing PromQL query %d for VPA %s/%s: %s", i+1, vpa.ID.Namespace, vpa.ID.VpaName, query)

		result, err := e.promClient.Query(ctx, query, time.Now())
		if err != nil {
			klog.Warningf("Failed to execute PromQL query for VPA %s/%s: %v", vpa.ID.Namespace, vpa.ID.VpaName, err)
			continue
		}

		containerResults, err := e.extractContainerMemoryFromResult(result)
		if err != nil {
			klog.Warningf("Failed to extract container memory from PromQL result for VPA %s/%s: %v", vpa.ID.Namespace, vpa.ID.VpaName, err)
			continue
		}

		// Aggregate maximum memory per container
		for _, containerResult := range containerResults {
			if existingMax, exists := containerMaxMemory[containerResult.ContainerName]; exists {
				if containerResult.MemoryBytes > existingMax {
					containerMaxMemory[containerResult.ContainerName] = containerResult.MemoryBytes
				}
			} else {
				containerMaxMemory[containerResult.ContainerName] = containerResult.MemoryBytes
			}
		}
	}

	// Convert map to slice
	var results []ContainerMemoryResult
	for containerName, memoryBytes := range containerMaxMemory {
		results = append(results, ContainerMemoryResult{
			ContainerName: containerName,
			MemoryBytes:   memoryBytes,
		})
	}

	if len(results) > 0 {
		klog.V(2).Infof("PromQL queries for VPA %s/%s returned memory recommendations for %d containers",
			vpa.ID.Namespace, vpa.ID.VpaName, len(results))
	}

	return results, nil
}

// extractContainerMemoryFromResult extracts container memory values from Prometheus query result
func (e *VPAPromQLExecutor) extractContainerMemoryFromResult(result prommodel.Value) ([]ContainerMemoryResult, error) {
	switch v := result.(type) {
	case prommodel.Scalar:
		return nil, fmt.Errorf("scalar results are not supported - use vector queries with container labels")
	case prommodel.Vector:
		var results []ContainerMemoryResult
		for _, sample := range v {
			memoryBytes, err := extractMemoryBytes(sample.Value)
			if err != nil {
				klog.V(4).Infof("Skipping sample with invalid memory value: %v", err)
				continue
			}

			containerName, err := extractContainerName(sample.Metric)
			if err != nil {
				klog.V(4).Infof("Skipping sample without valid container label: %v", err)
				continue
			}

			results = append(results, ContainerMemoryResult{
				ContainerName: containerName,
				MemoryBytes:   memoryBytes,
			})
		}
		return results, nil
	default:
		return nil, fmt.Errorf("unsupported result type: %T", result)
	}
}

// extractMemoryBytes extracts memory value in bytes from a Prometheus sample value
func extractMemoryBytes(value prommodel.SampleValue) (model.ResourceAmount, error) {
	bytes := float64(value)
	if bytes < 0 {
		return 0, fmt.Errorf("negative memory value: %f", bytes)
	}
	return model.ResourceAmount(bytes), nil
}

// extractContainerName extracts container name from metric labels
func extractContainerName(metric prommodel.Metric) (string, error) {
	// Try common container label names
	containerLabelNames := []string{"container", "container_name", "kubernetes_container_name"}

	for _, labelName := range containerLabelNames {
		if containerName, exists := metric[prommodel.LabelName(labelName)]; exists {
			containerNameStr := string(containerName)
			if containerNameStr != "" && containerNameStr != "POD" {
				return containerNameStr, nil
			}
		}
	}

	return "", fmt.Errorf("container label not found in metric")
}

// parsePromQLQueries parses a semicolon-separated string of PromQL queries
func parsePromQLQueries(queryString string) []string {
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
