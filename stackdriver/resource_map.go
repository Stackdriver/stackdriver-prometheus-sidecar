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

package stackdriver

import (
	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/prometheus/pkg/labels"
)

const (
	ProjectIdLabel = "_stackdriver_project_id"
)

// TODO(jkohen): ensure these are sorted from more specific to less specific.
var DefaultResourceMappings = []ResourceMap{
	{
		// This is just for testing, until the Kubernetes resource types are public.
		Type: "gke_container",
		LabelMap: map[string]string{
			ProjectIdLabel:                   "project_id",
			"_kubernetes_location":           "zone",
			"_kubernetes_cluster_name":       "cluster_name",
			"_kubernetes_namespace":          "namespace_id",
			"_kubernetes_pod_name":           "pod_id",
			"_kubernetes_pod_node_name":      "instance_id",
			"_kubernetes_pod_container_name": "container_name",
		},
	},
}

// TODO(jkohen): ensure these are sorted from more specific to less specific.
var K8sResourceMappings = []ResourceMap{
	{
		Type: "k8s_container",
		LabelMap: map[string]string{
			ProjectIdLabel:                         "project_id",
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
			ProjectIdLabel:                "project_id",
			"_kubernetes_location":        "location",
			"_kubernetes_cluster_name":    "cluster_name",
			"__meta_kubernetes_namespace": "namespace_name",
			"__meta_kubernetes_pod_name":  "pod_name",
		},
	},
	{
		Type: "k8s_node",
		LabelMap: map[string]string{
			ProjectIdLabel:                "project_id",
			"_kubernetes_location":        "location",
			"_kubernetes_cluster_name":    "cluster_name",
			"__meta_kubernetes_node_name": "node_name",
		},
	},
}

type ResourceMap struct {
	// The name of the Stackdriver MonitoredResource.
	Type string
	// Mapping from Prometheus to Stackdriver labels
	LabelMap map[string]string
}

func (m *ResourceMap) Translate(targetLabels labels.Labels, labels []*dto.LabelPair) map[string]string {
	stackdriverLabels := make(map[string]string, len(m.LabelMap))
	for i := range targetLabels {
		if stackdriverName, ok := m.LabelMap[targetLabels[i].Name]; ok {
			stackdriverLabels[stackdriverName] = targetLabels[i].Value
		}
	}
	for _, label := range labels {
		if stackdriverName, ok := m.LabelMap[label.GetName()]; ok {
			stackdriverLabels[stackdriverName] = label.GetValue()
		}
	}
	if len(m.LabelMap) == len(stackdriverLabels) {
		return stackdriverLabels
	} else {
		return nil
	}
}
