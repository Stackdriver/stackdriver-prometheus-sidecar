/*
Copyright 2017 Google Inc.
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

package retrieval

import "github.com/prometheus/prometheus/pkg/labels"

const ProjectIDLabel = "_stackdriver_project_id"

type ResourceMap struct {
	// The name of the Stackdriver MonitoredResource.
	Type string
	// Mapping from Prometheus to Stackdriver labels
	LabelMap map[string]string
}

// TODO(jkohen): ensure these are sorted from more specific to less specific.
var ResourceMappings = []ResourceMap{
	{
		Type: "k8s_container",
		LabelMap: map[string]string{
			ProjectIDLabel:                         "project_id",
			"_kubernetes_location":                 "location",
			"_kubernetes_cluster_name":             "cluster_name",
			"__meta_kubernetes_namespace":          "namespace_name",
			"__meta_kubernetes_pod_name":           "pod_name",
			"__meta_kubernetes_pod_container_name": "container_name",
		},
	},
	{
		Type: "k8s_pod",
		LabelMap: map[string]string{
			ProjectIDLabel:                "project_id",
			"_kubernetes_location":        "location",
			"_kubernetes_cluster_name":    "cluster_name",
			"__meta_kubernetes_namespace": "namespace_name",
			"__meta_kubernetes_pod_name":  "pod_name",
		},
	},
	{
		Type: "k8s_node",
		LabelMap: map[string]string{
			ProjectIDLabel:                "project_id",
			"_kubernetes_location":        "location",
			"_kubernetes_cluster_name":    "cluster_name",
			"__meta_kubernetes_node_name": "node_name",
		},
	},
}

func (m *ResourceMap) Translate(lset labels.Labels) map[string]string {
	stackdriverLabels := make(map[string]string, len(m.LabelMap))
	for _, l := range lset {
		if stackdriverName, ok := m.LabelMap[l.Name]; ok {
			stackdriverLabels[stackdriverName] = l.Value
		}
	}
	if len(m.LabelMap) == len(stackdriverLabels) {
		return stackdriverLabels
	}
	return nil
}
