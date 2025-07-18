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
	"testing"

	prommodel "github.com/prometheus/common/model"
	"github.com/stretchr/testify/assert"
	"k8s.io/autoscaler/vertical-pod-autoscaler/pkg/recommender/model"
)

func TestParsePromQLQueries(t *testing.T) {
	testCases := []struct {
		name     string
		input    string
		expected []string
	}{
		{
			name:     "empty string",
			input:    "",
			expected: nil,
		},
		{
			name:     "single query",
			input:    "avg_over_time(container_memory_working_set_bytes[5m])",
			expected: []string{"avg_over_time(container_memory_working_set_bytes[5m])"},
		},
		{
			name:     "multiple queries",
			input:    "query1; query2; query3",
			expected: []string{"query1", "query2", "query3"},
		},
		{
			name:     "queries with spaces",
			input:    " query1 ; query2 ; query3 ",
			expected: []string{"query1", "query2", "query3"},
		},
		{
			name:  "complex queries",
			input: "avg_over_time(container_memory_working_set_bytes{namespace=\"default\"}[5m]); quantile_over_time(0.95, container_memory_working_set_bytes[1h])",
			expected: []string{
				"avg_over_time(container_memory_working_set_bytes{namespace=\"default\"}[5m])",
				"quantile_over_time(0.95, container_memory_working_set_bytes[1h])",
			},
		},
		{
			name:     "empty entries filtered",
			input:    "query1;;query2; ;query3",
			expected: []string{"query1", "query2", "query3"},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := ParsePromQLQueries(tc.input)
			assert.Equal(t, tc.expected, result)
		})
	}
}

func TestExtractPodID(t *testing.T) {
	client := &promqlClient{}

	testCases := []struct {
		name      string
		metric    prommodel.Metric
		expected  *model.PodID
		expectErr bool
	}{
		{
			name: "standard pod and namespace labels",
			metric: prommodel.Metric{
				"pod":       "test-pod",
				"namespace": "test-namespace",
			},
			expected: &model.PodID{
				Namespace: "test-namespace",
				PodName:   "test-pod",
			},
			expectErr: false,
		},
		{
			name: "kubernetes style labels",
			metric: prommodel.Metric{
				"kubernetes_pod_name":  "test-pod",
				"kubernetes_namespace": "test-namespace",
			},
			expected: &model.PodID{
				Namespace: "test-namespace",
				PodName:   "test-pod",
			},
			expectErr: false,
		},
		{
			name: "pod_name style labels",
			metric: prommodel.Metric{
				"pod_name":  "test-pod",
				"namespace": "test-namespace",
			},
			expected: &model.PodID{
				Namespace: "test-namespace",
				PodName:   "test-pod",
			},
			expectErr: false,
		},
		{
			name: "missing pod name",
			metric: prommodel.Metric{
				"namespace": "test-namespace",
			},
			expected:  nil,
			expectErr: true,
		},
		{
			name: "missing namespace",
			metric: prommodel.Metric{
				"pod": "test-pod",
			},
			expected:  nil,
			expectErr: true,
		},
		{
			name:      "empty labels",
			metric:    prommodel.Metric{},
			expected:  nil,
			expectErr: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := client.extractPodID(tc.metric)

			if tc.expectErr {
				assert.Error(t, err)
				assert.Nil(t, result)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tc.expected, result)
			}
		})
	}
}
