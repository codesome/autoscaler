# PromQL Memory Recommendation Feature

This document describes the new PromQL-based memory recommendation feature added to the VPA recommender.

## Overview

The VPA recommender now supports using custom PromQL queries for memory recommendations instead of the default histogram-based calculations. This allows for more flexible and custom memory recommendation strategies based on your specific metrics and requirements.

## Usage

### Flag

Use the `--promql-queries` flag to specify custom PromQL queries for memory recommendations (semicolon-separated):

```bash
# Single query
./recommender --promql-queries="avg_over_time(container_memory_working_set_bytes{pod=\"my-pod\"}[5m])"

# Multiple queries (semicolon-separated)
./recommender --promql-queries="avg_over_time(container_memory_working_set_bytes[5m]); quantile_over_time(0.95, container_memory_working_set_bytes[1h])"
```

### Requirements

1. **Prometheus Connection**: The recommender must be able to connect to Prometheus using the existing Prometheus configuration flags:
   - `--prometheus-address` (default: `http://prometheus.monitoring.svc`)
   - `--prometheus-bearer-token` or `--username`/`--password` for authentication
   - `--prometheus-insecure` for TLS verification

2. **Query Result**: The PromQL query must return either:
   - A scalar value representing memory usage in bytes
   - A vector with at least one sample representing memory usage in bytes

### Example Queries

#### Simple Average Memory Usage
```promql
avg_over_time(container_memory_working_set_bytes{namespace="default", pod="my-app"}[10m])
```

#### Percentile-Based Memory Usage
```promql
quantile_over_time(0.95, container_memory_working_set_bytes{namespace="default", pod="my-app"}[1h])
```

#### Custom Memory Calculation
```promql
max_over_time(container_memory_working_set_bytes{namespace="default", pod="my-app"}[24h]) * 1.2
```

### Configuration Example

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: vpa-recommender
spec:
  template:
    spec:
      containers:
      - name: recommender
        image: k8s.gcr.io/autoscaling/vpa-recommender:latest
        command:
        - ./recommender
        - --v=4
        - --prometheus-address=http://prometheus.monitoring.svc:9090
        - --promql-queries=quantile_over_time(0.95, container_memory_working_set_bytes{namespace="default"}[1h])
```

## How It Works

1. **Query Execution**: When the `--memory-promql-query` flag is provided, the recommender creates a custom memory estimator that executes the PromQL query against Prometheus.

2. **Safety Margins**: The PromQL result is still processed through the standard safety margin calculations (15% by default) and confidence multipliers.

3. **Fallback**: If the PromQL query fails or returns invalid results, the memory recommendation falls back to 0 bytes with appropriate logging.

4. **CPU Recommendations**: CPU recommendations continue to use the standard histogram-based approach regardless of the PromQL memory configuration.

## Important Notes

- The PromQL query is executed for every container recommendation request
- Query performance impacts recommender performance
- The query should be designed to be container/pod-specific if needed
- Memory values are expected to be in bytes
- Negative values are automatically converted to 0 bytes

## Migration

To migrate from histogram-based to PromQL-based memory recommendations:

1. Identify your current memory recommendation requirements
2. Create appropriate PromQL queries that match your needs
3. Test the queries in Prometheus to ensure they return expected values
4. Update your VPA recommender deployment with the new flag
5. Monitor logs for query execution success/failure 