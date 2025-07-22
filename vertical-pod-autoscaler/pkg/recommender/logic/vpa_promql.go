package logic

import (
	"context"
	"fmt"
	"strings"
	"time"

	promapi "github.com/prometheus/client_golang/api"
	promv1 "github.com/prometheus/client_golang/api/prometheus/v1"
	prommodel "github.com/prometheus/common/model"
	"k8s.io/klog/v2"

	"k8s.io/autoscaler/vertical-pod-autoscaler/pkg/recommender/input/history"
	"k8s.io/autoscaler/vertical-pod-autoscaler/pkg/recommender/model"
)

const (
	// VPA annotation prefix for PromQL queries
	VPAPromQLQueriesAnnotationPrefix = "vpa.k8s.io/promql-memory-queries."
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
func (e *VPAPromQLExecutor) ExecuteVPAPromQLQueries(ctx context.Context, vpa *model.Vpa, skipContainer func(containerName string) bool) ([]ContainerMemoryResult, error) {
	if vpa.Annotations == nil {
		return nil, nil
	}

	// Find all PromQL query annotations with the format "vpa.k8s.io/promql-memory-queries/<container_name>"
	containerQueries := make(map[string]string)
	for annotationKey, query := range vpa.Annotations {
		if strings.HasPrefix(annotationKey, VPAPromQLQueriesAnnotationPrefix) {
			containerName := strings.TrimPrefix(annotationKey, VPAPromQLQueriesAnnotationPrefix)
			if containerName != "" && query != "" {
				containerQueries[containerName] = query
			}
		}
	}

	if len(containerQueries) == 0 {
		return nil, nil
	}

	klog.V(2).Infof("Executing PromQL queries for %d containers in VPA %s/%s", len(containerQueries), vpa.ID.Namespace, vpa.ID.VpaName)

	// Track maximum memory per container across all queries
	containerMaxMemory := make(map[string]model.ResourceAmount)

	for containerName, queryString := range containerQueries {
		if skipContainer(containerName) {
			continue
		}
		// Parse semicolon-separated queries for this container
		queries := parsePromQLQueries(queryString)
		if len(queries) == 0 {
			continue
		}

		klog.V(3).Infof("Container %s in VPA %s/%s has %d PromQL queries", containerName, vpa.ID.Namespace, vpa.ID.VpaName, len(queries))

		for i, query := range queries {
			klog.V(4).Infof("Executing PromQL query %d for container %s in VPA %s/%s: %s", i+1, containerName, vpa.ID.Namespace, vpa.ID.VpaName, query)

			result, _, err := e.promClient.Query(ctx, query, time.Now())
			if err != nil {
				klog.Warningf("Failed to execute PromQL query %d for container %s in VPA %s/%s: %v", i+1, containerName, vpa.ID.Namespace, vpa.ID.VpaName, err)
				continue
			}

			resMemory, err := e.extractMemoryFromPromqlResult(result)
			if err != nil {
				klog.Warningf("Failed to extract container memory from PromQL result %d for container %s in VPA %s/%s: %v", i+1, containerName, vpa.ID.Namespace, vpa.ID.VpaName, err)
				continue
			}

			// Track maximum for this container
			if resMemory > containerMaxMemory[containerName] {
				containerMaxMemory[containerName] = resMemory
				klog.V(4).Infof("Updated max memory for container %s: %d bytes (from query %d)", containerName, resMemory, i+1)
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
		klog.V(3).Infof("Final memory recommendation for container %s in VPA %s/%s: %d bytes", containerName, vpa.ID.Namespace, vpa.ID.VpaName, memoryBytes)
	}

	if len(results) > 0 {
		klog.V(2).Infof("PromQL queries for VPA %s/%s returned memory recommendations for %d containers",
			vpa.ID.Namespace, vpa.ID.VpaName, len(results))
	}

	return results, nil
}

// extractMemoryFromPromqlResult extracts container memory values from Prometheus query result
func (e *VPAPromQLExecutor) extractMemoryFromPromqlResult(result prommodel.Value) (model.ResourceAmount, error) {
	switch v := result.(type) {
	case *prommodel.Scalar:
		// Accept scalar values and apply as memory recommendation
		memoryBytes, err := extractMemoryBytes(v.Value)
		if err != nil {
			return 0, fmt.Errorf("invalid memory value in scalar result: %v", err)
		}

		if memoryBytes < 0 {
			return 0, fmt.Errorf("negative invalid memory value in scalar result: %d", memoryBytes)
		}

		return memoryBytes, nil
	case prommodel.Vector:
		// Ensure vector has only 1 sample
		if len(v) != 1 {
			return 0, fmt.Errorf("vector result must contain exactly 1 sample, got %d samples", len(v))
		}

		memoryBytes, err := extractMemoryBytes(v[0].Value)
		if err != nil {
			return 0, fmt.Errorf("invalid memory value in vector sample: %v", err)
		}

		if memoryBytes < 0 {
			return 0, fmt.Errorf("negative invalid memory value in vector sample: %d", memoryBytes)
		}

		// Apply the memory recommendation without checking container names
		return memoryBytes, nil
	default:
		return 0, fmt.Errorf("unsupported result type: %T", result)
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

// HasPromQLQueries checks if the VPA has any PromQL query annotations
func HasPromQLQueries(vpa *model.Vpa) bool {
	if vpa.Annotations == nil {
		return false
	}

	for annotationKey := range vpa.Annotations {
		if strings.HasPrefix(annotationKey, VPAPromQLQueriesAnnotationPrefix) {
			return true
		}
	}
	return false
}

// GetPromQLContainers returns the list of container names that have PromQL queries
func GetPromQLContainers(vpa *model.Vpa) []string {
	if vpa.Annotations == nil {
		return nil
	}

	var containers []string
	for annotationKey, query := range vpa.Annotations {
		if strings.HasPrefix(annotationKey, VPAPromQLQueriesAnnotationPrefix) {
			containerName := strings.TrimPrefix(annotationKey, VPAPromQLQueriesAnnotationPrefix)
			if containerName != "" && query != "" {
				containers = append(containers, containerName)
			}
		}
	}
	return containers
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
