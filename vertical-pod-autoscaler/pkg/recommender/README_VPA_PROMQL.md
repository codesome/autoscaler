# VPA Annotation-Based PromQL Memory Recommendations

## Overview

The VPA recommender now supports overriding memory recommendations using PromQL queries specified in VPA annotations. This feature allows you to use custom metrics from Prometheus to determine memory requirements for your containers, completely bypassing the default histogram-based memory estimation.

## Key Features

- **Annotation-driven**: PromQL queries are specified directly in VPA annotations
- **Container-specific**: Query results are mapped to specific containers using the `container` label
- **Maximum aggregation**: Multiple queries are executed and the maximum memory value across all queries is used per container
- **Complete override**: PromQL-based recommendations completely replace histogram-based memory estimates (target, lower bound, and upper bound)
- **Fallback support**: If no PromQL queries are specified or they fail, the system falls back to standard histogram-based recommendations

## Usage

### VPA Annotation

Add PromQL queries to your VPA object using the following annotation:

```yaml
apiVersion: autoscaling.k8s.io/v1
kind: VerticalPodAutoscaler
metadata:
  name: my-app-vpa
  annotations:
    vpa.k8s.io/promql-memory-queries: "query1; query2; query3"
spec:
  targetRef:
    apiVersion: apps/v1
    kind: Deployment
    name: my-app
  updatePolicy:
    updateMode: "Auto"
```

### Query Format

- **Multiple queries**: Separate multiple PromQL queries with semicolons (`;`)
- **Container label requirement**: Each query must return results with a `container` label indicating which container the memory value applies to
- **Memory unit**: Query results should return memory values in bytes

### Example Queries

```yaml
annotations:
  # Single query example
  vpa.k8s.io/promql-memory-queries: "max_over_time(container_memory_working_set_bytes{namespace=\"default\", pod=~\"my-app-.*\"}[1h])"
  
  # Multiple queries example (max across all will be used)
  vpa.k8s.io/promql-memory-queries: "max_over_time(container_memory_working_set_bytes{namespace=\"default\", pod=~\"my-app-.*\"}[1h]); max_over_time(container_memory_rss{namespace=\"default\", pod=~\"my-app-.*\"}[2h]); quantile_over_time(0.95, container_memory_usage_bytes{namespace=\"default\", pod=~\"my-app-.*\"}[24h])"
```

### Container Label Mapping

The system looks for container names in the following metric labels (in order of precedence):
1. `container`
2. `container_name` 
3. `kubernetes_container_name`

Make sure your PromQL queries return results with one of these labels containing the target container name.

## How It Works

### Execution Flow

1. **Standard Recommendations**: The VPA recommender first calculates standard histogram-based memory recommendations for all containers
2. **PromQL Execution**: If the VPA has the `vpa.k8s.io/promql-memory-queries` annotation, the system:
   - Parses the semicolon-separated PromQL queries
   - Executes each query against the configured Prometheus instance
   - Extracts container names and memory values from the results
   - Calculates the maximum memory value per container across all queries
3. **Override**: For containers that have PromQL results, the system completely overrides the histogram-based memory recommendations with the PromQL-derived maximum values
4. **Fallback**: Containers without PromQL results continue to use histogram-based recommendations

### Memory Recommendation Override

When PromQL data is available for a container, **all three memory recommendation values are set to the same value**:
- `Target`: Set to PromQL maximum
- `LowerBound`: Set to PromQL maximum  
- `UpperBound`: Set to PromQL maximum

This approach provides a simple, deterministic memory recommendation based purely on your custom metrics.

## Configuration

### Prometheus Connection

The system uses the existing Prometheus configuration flags:
- `--prometheus-address`: Prometheus server address (default: `http://prometheus.monitoring.svc`)
- `--prometheus-insecure`: Skip TLS verification
- `--prometheus-query-timeout`: Query timeout duration
- `--prometheus-bearer-token`: Bearer token for authentication
- `--username` / `--password`: Basic authentication

### Logging

The system provides detailed logging for PromQL operations:
- `V(1)`: VPA PromQL executor creation status
- `V(2)`: PromQL query execution and memory override notifications
- `V(3)`: Detailed query-by-query execution logs
- `V(4)`: Individual metric sample processing details

## Example Use Cases

### 1. Peak Memory Usage Over Time Window
```yaml
vpa.k8s.io/promql-memory-queries: "max_over_time(container_memory_working_set_bytes{namespace=\"production\", pod=~\"api-.*\"}[24h])"
```

### 2. High Percentile with Multiple Metrics
```yaml
vpa.k8s.io/promql-memory-queries: "quantile_over_time(0.95, container_memory_working_set_bytes{namespace=\"production\", pod=~\"api-.*\"}[7d]); quantile_over_time(0.90, container_memory_rss{namespace=\"production\", pod=~\"api-.*\"}[7d])"
```

### 3. Custom Application Metrics
```yaml
vpa.k8s.io/promql-memory-queries: "max_over_time(jvm_memory_used_bytes{namespace=\"production\", pod=~\"java-app-.*\"}[12h]); max_over_time(go_memstats_alloc_bytes{namespace=\"production\", pod=~=\"java-app-.*\"}[12h])"
```

## Troubleshooting

### Common Issues

1. **No container label in results**: Ensure your PromQL queries return metrics with `container`, `container_name`, or `kubernetes_container_name` labels
2. **Query timeout**: Increase `--prometheus-query-timeout` for complex queries
3. **Authentication failures**: Verify Prometheus credentials and network connectivity
4. **No override applied**: Check that container names in PromQL results exactly match container names in your VPA target workload

### Monitoring

Monitor the VPA recommender logs for:
```
# Successful PromQL execution
"VPA PromQL executor created successfully"
"Executing N PromQL queries for VPA namespace/name"
"PromQL queries for VPA namespace/name returned memory recommendations for N containers"
"Overriding memory recommendation for container X in VPA namespace/name: Y bytes (from PromQL)"

# Error conditions
"Failed to create VPA PromQL executor: <error>"
"Failed to execute PromQL queries for VPA namespace/name: <error>"
"Failed to extract container memory from PromQL result for VPA namespace/name: <error>"
```

## Comparison with Previous Implementation

| Feature | Old (CLI-based) | New (Annotation-based) |
|---------|----------------|------------------------|
| **Configuration** | CLI flags | VPA annotations |
| **Scope** | Global queries for all VPAs | Per-VPA custom queries |
| **Execution timing** | During metrics collection | During recommendation calculation |
| **Data storage** | Pod-level tracking in ClusterState | Direct application to recommendations |
| **Flexibility** | Fixed queries for all workloads | Custom queries per VPA |
| **Maintenance** | Requires recommender restart for query changes | Dynamic via annotation updates |

The new annotation-based approach provides much greater flexibility and eliminates the need to restart the recommender when changing PromQL queries. 