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

const (
	ProjectIDLabel             = "_stackdriver_project_id"
	KubernetesLocationLabel    = "_kubernetes_location"
	KubernetesClusterNameLabel = "_kubernetes_cluster_name"
	GenericNamespaceLabel      = "_generic_namespace"
	GenericLocationLabel       = "_generic_location"
)

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
	// MatchLabel must exist in the set of Prometheus labels in order for this map to match. Ignored if empty.
	MatchLabel string
	// Mapping from Prometheus to Stackdriver labels
	LabelMap map[string]labelTranslation
}

var EC2ResourceMap = ResourceMap{
	Type: "aws_ec2_instance",
	LabelMap: map[string]labelTranslation{
		ProjectIDLabel:           constValue("project_id"),
		"__meta_ec2_instance_id": constValue("instance_id"),
		"__meta_ec2_availability_zone": labelTranslation{
			stackdriverLabelName: "region",
			convert: func(s string) string {
				return "aws:" + s
			},
		},
		"__meta_ec2_owner_id": constValue("aws_account"),
	},
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

var GKEResourceMap = ResourceMap{
	Type: "gke_container",
	LabelMap: map[string]labelTranslation{
		ProjectIDLabel:                         constValue("project_id"),
		KubernetesLocationLabel:                constValue("zone"),
		KubernetesClusterNameLabel:             constValue("cluster_name"),
		"__meta_kubernetes_namespace":          constValue("namespace_id"),
		"__meta_kubernetes_node_name":          constValue("instance_id"),
		"__meta_kubernetes_pod_name":           constValue("pod_id"),
		"__meta_kubernetes_pod_container_name": constValue("container_name"),
	},
}

var DevappResourceMap = ResourceMap{
	Type:       "apigee.googleapis.com/Devapp",
	MatchLabel: "__meta_kubernetes_pod_label_type_devapp",
	LabelMap: map[string]labelTranslation{
		ProjectIDLabel:                    constValue("resource_container"),
		KubernetesLocationLabel:           constValue("location"),
		"__meta_kubernetes_pod_label_org": constValue("org"),
		"__meta_kubernetes_pod_label_env": constValue("env"),
		"api_product_name":                constValue("api_product_name"),
		"__meta_kubernetes_pod_name":      constValue("host"),
	},
}

var ProxyResourceMap = ResourceMap{
	Type:       "apigee.googleapis.com/Proxy",
	MatchLabel: "__meta_kubernetes_pod_label_type_proxy",
	LabelMap: map[string]labelTranslation{
		ProjectIDLabel:                    constValue("resource_container"),
		KubernetesLocationLabel:           constValue("location"),
		"__meta_kubernetes_pod_label_org": constValue("org"),
		"__meta_kubernetes_pod_label_env": constValue("env"),
		"proxy_name":                      constValue("proxy_name"),
		"revision":                        constValue("revision"),
		"__meta_kubernetes_pod_name":      constValue("host"),
	},
}

var ProxyV2ResourceMap = ResourceMap{
	Type:       "apigee.googleapis.com/ProxyV2",
	MatchLabel: "__meta_kubernetes_pod_label_type_proxy_v2",
	LabelMap: map[string]labelTranslation{
		ProjectIDLabel:          constValue("resource_container"),
		KubernetesLocationLabel: constValue("location"),
		"org":                   constValue("org"),
		"env":                   constValue("env"),
		"proxy_name":            constValue("proxy_name"),
		"runtime_version":       constValue("runtime_version"),
		"instance_id":           constValue("instance_id"),
	},
}

var AnthosL4LBMap = ResourceMap{
	Type:       "anthos_l4lb",
	MatchLabel: "__meta_kubernetes_service_annotation_gke_googleapis_com_anthos_l4lb_type",
	LabelMap: map[string]labelTranslation{
		ProjectIDLabel:          constValue("project_id"),
		KubernetesLocationLabel: constValue("location"),
		"__meta_kubernetes_service_annotation_gke_googleapis_com_anthos_l4lb_kind":       constValue("kind"),
		"__meta_kubernetes_service_annotation_gke_googleapis_com_anthos_l4lb_group_name": constValue("group_name"),
		"__meta_kubernetes_service_annotation_gke_googleapis_com_anthos_l4lb_hostname":   constValue("hostname"),
	},
}

type ResourceMapList []ResourceMap

// When you add new elements, you also probably want to update TestResourceMappingsOrder.
var ResourceMappings = ResourceMapList{
	AnthosL4LBMap,
	ProxyV2ResourceMap,
	ProxyResourceMap,
	DevappResourceMap,
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
	EC2ResourceMap,
	GCEResourceMap,
	{
		Type: "generic_task",
		LabelMap: map[string]labelTranslation{
			ProjectIDLabel:        constValue("project_id"),
			GenericLocationLabel:  constValue("location"),
			GenericNamespaceLabel: constValue("namespace"),
			"job":                 constValue("job"),
			"instance":            constValue("task_id"),
		},
	},
}

// Translate translates labels to a monitored resource and entry labels, if
// possible. Returns the resource and the modified entry labels.
//
// The labels in `discovered` and `entryLabels` are used as input. If a label
// exists in both sets, the one in `entryLabels` takes precedence. Whenever a
// label from `entryLabels` is used, it is removed from the set that is
// returned.
func (m *ResourceMap) Translate(discovered, entryLabels labels.Labels) (map[string]string, labels.Labels) {
	stackdriverLabels, entryLabels := m.tryTranslate(discovered, entryLabels)
	if len(m.LabelMap) == len(stackdriverLabels) {
		return stackdriverLabels, entryLabels
	}
	return nil, nil
}

// BestEffortTranslate translates labels to resource with best effort. If the resource label
// cannot be filled, use empty string instead.
func (m *ResourceMap) BestEffortTranslate(discovered, entryLabels labels.Labels) (map[string]string, labels.Labels) {
	stackdriverLabels, entryLabels := m.tryTranslate(discovered, entryLabels)
	for _, t := range m.LabelMap {
		if _, ok := stackdriverLabels[t.stackdriverLabelName]; !ok {
			stackdriverLabels[t.stackdriverLabelName] = ""
		}
	}
	return stackdriverLabels, entryLabels
}

func (m *ResourceMap) tryTranslate(discovered, entryLabels labels.Labels) (map[string]string, labels.Labels) {
	matched := false
	stackdriverLabels := make(map[string]string, len(m.LabelMap))
	for _, l := range discovered {
		if l.Name == m.MatchLabel {
			matched = true
		}
		if translator, ok := m.LabelMap[l.Name]; ok {
			stackdriverLabels[translator.stackdriverLabelName] = translator.convert(l.Value)
		}
	}
	// The entryLabels labels are applied second so they overwrite mappings from discovered labels.
	// This ensures, that the Prometheus's relabeling rules are respected for labels that
	// appear in both label sets, e.g. the "job" label for generic resources.
	var finalLabels labels.Labels
	for _, l := range entryLabels {
		if translator, ok := m.LabelMap[l.Name]; ok {
			stackdriverLabels[translator.stackdriverLabelName] = translator.convert(l.Value)
		} else {
			finalLabels = append(finalLabels, l)
		}
	}
	if len(m.MatchLabel) > 0 && !matched {
		return nil, finalLabels
	}
	return stackdriverLabels, finalLabels
}
