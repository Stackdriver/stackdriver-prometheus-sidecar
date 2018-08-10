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

import (
	"strings"

	"github.com/prometheus/prometheus/pkg/labels"
)

const ProjectIDLabel = "_stackdriver_project_id"
const KubernetesLocationLabel = "_kubernetes_location"
const KubernetesClusterNameLabel = "_kubernetes_cluster_name"

type labelTranslation struct {
	stackdriverLabelName string
	convert              func(string) string
}

func constValue(labelName string) labelTranslation {
	return labelTranslation{
		stackdriverLabelName: labelName,
		convert:              func(s string) string { return s },
	}
}

type ResourceMap struct {
	// The name of the Stackdriver MonitoredResource.
	Type string
	// Mapping from Prometheus to Stackdriver labels
	LabelMap map[string]labelTranslation
}

var GCEResourceMap = ResourceMap{
	Type: "gce_instance",
	LabelMap: map[string]labelTranslation{
		"__meta_gce_project":     constValue("project_id"),
		"__meta_gce_instance_id": constValue("instance_id"),
		"__meta_gce_zone": labelTranslation{
			stackdriverLabelName: "zone",
			convert: func(s string) string {
				return s[strings.LastIndex(s, "/")+1:]
			},
		},
	},
}

// TODO(jkohen): ensure these are sorted from more specific to less specific.
var ResourceMappings = []ResourceMap{
	{
		Type: "debug",
		LabelMap: map[string]labelTranslation{
			"_debug":      constValue("debug"),
			"__address__": constValue("address"),
			"job":         constValue("job"),
		},
	},
	{
		Type: "k8s_container",
		LabelMap: map[string]labelTranslation{
			ProjectIDLabel:                         constValue("project_id"),
			KubernetesLocationLabel:                constValue("location"),
			KubernetesClusterNameLabel:             constValue("cluster_name"),
			"__meta_kubernetes_namespace":          constValue("namespace_name"),
			"__meta_kubernetes_pod_name":           constValue("pod_name"),
			"__meta_kubernetes_pod_container_name": constValue("container_name"),
		},
	},
	{
		Type: "k8s_pod",
		LabelMap: map[string]labelTranslation{
			ProjectIDLabel:                constValue("project_id"),
			KubernetesLocationLabel:       constValue("location"),
			KubernetesClusterNameLabel:    constValue("cluster_name"),
			"__meta_kubernetes_namespace": constValue("namespace_name"),
			"__meta_kubernetes_pod_name":  constValue("pod_name"),
		},
	},
	{
		Type: "k8s_node",
		LabelMap: map[string]labelTranslation{
			ProjectIDLabel:                constValue("project_id"),
			KubernetesLocationLabel:       constValue("location"),
			KubernetesClusterNameLabel:    constValue("cluster_name"),
			"__meta_kubernetes_node_name": constValue("node_name"),
		},
	},
	GCEResourceMap,
}

func (m *ResourceMap) Translate(lset labels.Labels) map[string]string {
	stackdriverLabels := make(map[string]string, len(m.LabelMap))
	for _, l := range lset {
		if translator, ok := m.LabelMap[l.Name]; ok {
			stackdriverLabels[translator.stackdriverLabelName] = translator.convert(l.Value)
		}
	}
	if len(m.LabelMap) == len(stackdriverLabels) {
		return stackdriverLabels
	}
	return nil
}
