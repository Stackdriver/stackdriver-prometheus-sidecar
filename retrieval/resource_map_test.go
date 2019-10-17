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
	"reflect"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/prometheus/prometheus/pkg/labels"
)

func TestTranslate(t *testing.T) {
	r := ResourceMap{
		Type:       "my_type",
		MatchLabel: "__match_type",
		LabelMap: map[string]labelTranslation{
			"__target1": constValue("sdt1"),
			"__target2": constValue("sdt2"),
			"__target3": constValue("sdt3"),
		},
	}
	// This target is missing label "__target1".
	noMatchTarget := labels.Labels{
		{"ignored", "x"},
		{"__target2", "y"},
		{"__match_type", "true"},
	}
	if labels, _ := r.Translate(noMatchTarget, nil); labels != nil {
		t.Errorf("Expected no match, matched %v", labels)
	}
	matchTargetDiscovered := labels.Labels{
		{"ignored", "x"},
		{"__target2", "y"},
		{"__target1", "z"},
		{"__match_type", "true"},
	}
	matchTargetFinal := labels.Labels{
		{"__target1", "z2"},
		{"__target3", "v"},
		{"__match_type", "true"},
	}
	expectedLabels := map[string]string{
		"sdt1": "z2",
		"sdt2": "y",
		"sdt3": "v",
	}
	if labels, _ := r.Translate(matchTargetDiscovered, matchTargetFinal); labels == nil {
		t.Errorf("Expected %v, actual nil", expectedLabels)
	} else if !reflect.DeepEqual(labels, expectedLabels) {
		t.Errorf("Expected %v, actual %v", expectedLabels, labels)
	}
	missingType := labels.Labels{
		{"__target1", "x"},
		{"__target2", "y"},
		{"__target3", "z"},
	}
	if labels, _ := r.Translate(missingType, nil); labels != nil {
		t.Errorf("Expected no match, matched %v", labels)
	}
}

func TestTranslateEc2Instance(t *testing.T) {
	target := labels.Labels{
		{ProjectIDLabel, "my-project"},
		{"__meta_ec2_availability_zone", "us-east-1b"},
		{"__meta_ec2_instance_id", "i-040c37401c02cb03b"},
		{"__meta_ec2_owner_id", "12345678"},
	}
	expectedLabels := map[string]string{
		"project_id":  "my-project",
		"instance_id": "i-040c37401c02cb03b",
		"region":      "aws:us-east-1b",
		"aws_account": "12345678",
	}
	if labels, _ := EC2ResourceMap.Translate(target, nil); labels == nil {
		t.Errorf("Expected %v, actual nil", expectedLabels)
	} else if !reflect.DeepEqual(labels, expectedLabels) {
		t.Errorf("Expected %v, actual %v", expectedLabels, labels)
	}
}

func TestTranslateGceInstance(t *testing.T) {
	target := labels.Labels{
		{"__meta_gce_project", "my-project"},
		{"__meta_gce_zone", "https://www.googleapis.com/compute/v1/projects/my-project/zones/us-central1-a"},
		{"__meta_gce_instance_id", "1234110975759588"},
	}
	expectedLabels := map[string]string{
		"project_id":  "my-project",
		"zone":        "us-central1-a",
		"instance_id": "1234110975759588",
	}
	if labels, _ := GCEResourceMap.Translate(target, nil); labels == nil {
		t.Errorf("Expected %v, actual nil", expectedLabels)
	} else if !reflect.DeepEqual(labels, expectedLabels) {
		t.Errorf("Expected %v, actual %v", expectedLabels, labels)
	}
}

func TestBestEffortTranslate(t *testing.T) {
	target := labels.Labels{
		{ProjectIDLabel, "my-project"},
		{KubernetesLocationLabel, "us-central1-a"},
		{KubernetesClusterNameLabel, "cluster"},
	}
	expectedLabels := map[string]string{
		"project_id":     "my-project",
		"zone":           "us-central1-a",
		"cluster_name":   "cluster",
		"namespace_id":   "",
		"instance_id":    "",
		"pod_id":         "",
		"container_name": "",
	}
	if labels, _ := GKEResourceMap.BestEffortTranslate(target, nil); labels == nil {
		t.Errorf("Expected %v, actual nil", expectedLabels)
	} else if !reflect.DeepEqual(labels, expectedLabels) {
		t.Errorf("Expected %v, actual %v", expectedLabels, labels)
	}
}

func TestTranslateDevapp(t *testing.T) {
	discoveredLabels := labels.Labels{
		{"__meta_kubernetes_pod_label_type_devapp", "true"},
		{ProjectIDLabel, "my-project"},
		{KubernetesLocationLabel, "us-central1-a"},
		{"__meta_kubernetes_pod_label_org", "my-org"},
		{"__meta_kubernetes_pod_label_env", "my-env"},
	}
	metricLabels := labels.Labels{
		{"api_product_name", "my-name"},
		{"extra_label", "my-label"},
	}
	expectedLabels := map[string]string{
		"resource_container": "my-project",
		"location":           "us-central1-a",
		"org":                "my-org",
		"env":                "my-env",
		"api_product_name":   "my-name",
	}
	expectedFinalLabels := labels.Labels{
		{"extra_label", "my-label"},
	}
	if labels, finalLabels := DevappResourceMap.Translate(discoveredLabels, metricLabels); labels == nil {
		t.Errorf("Expected %v, actual nil", expectedLabels)
	} else {
		if diff := cmp.Diff(expectedLabels, labels); len(diff) > 0 {
			t.Error(diff)
		}
		if diff := cmp.Diff(expectedFinalLabels, finalLabels); len(diff) > 0 {
			t.Error(diff)
		}
	}
}

func TestTranslateProxy(t *testing.T) {
	discoveredLabels := labels.Labels{
		{"__meta_kubernetes_pod_label_type_proxy", "true"},
		{ProjectIDLabel, "my-project"},
		{KubernetesLocationLabel, "us-central1-a"},
		{"__meta_kubernetes_pod_label_org", "my-org"},
		{"__meta_kubernetes_pod_label_env", "my-env"},
	}
	metricLabels := labels.Labels{
		{"proxy_name", "my-name"},
		{"revision", "my-revision"},
		{"extra_label", "my-label"},
	}
	expectedLabels := map[string]string{
		"resource_container": "my-project",
		"location":           "us-central1-a",
		"org":                "my-org",
		"env":                "my-env",
		"proxy_name":         "my-name",
		"revision":           "my-revision",
	}
	expectedFinalLabels := labels.Labels{
		{"extra_label", "my-label"},
	}
	if labels, finalLabels := ProxyResourceMap.Translate(discoveredLabels, metricLabels); labels == nil {
		t.Errorf("Expected %v, actual nil", expectedLabels)
	} else {
		if diff := cmp.Diff(expectedLabels, labels); len(diff) > 0 {
			t.Error(diff)
		}
		if diff := cmp.Diff(expectedFinalLabels, finalLabels); len(diff) > 0 {
			t.Error(diff)
		}
	}
}

func BenchmarkTranslate(b *testing.B) {
	r := ResourceMap{
		Type: "gke_container",
		LabelMap: map[string]labelTranslation{
			ProjectIDLabel:                   constValue("project_id"),
			KubernetesLocationLabel:          constValue("zone"),
			KubernetesClusterNameLabel:       constValue("cluster_name"),
			"_kubernetes_namespace":          constValue("namespace_id"),
			"_kubernetes_pod_name":           constValue("pod_id"),
			"_kubernetes_pod_node_name":      constValue("instance_id"),
			"_kubernetes_pod_container_name": constValue("container_name"),
		},
	}
	discoveredLabels := labels.Labels{
		{ProjectIDLabel, "1:anoeuh oeusoeh uasoeuh"},
		{KubernetesLocationLabel, "2:anoeuh oeusoeh uasoeuh"},
		{KubernetesClusterNameLabel, "3:anoeuh oeusoeh uasoeuh"},
		{"_kubernetes_namespace", "4:anoeuh oeusoeh uasoeuh"},
		{"_kubernetes_pod_name", "5:anoeuh oeusoeh uasoeuh"},
		{"_kubernetes_pod_node_name", "6:anoeuh oeusoeh uasoeuh"},
		{"_kubernetes_pod_container_name", "7:anoeuh oeusoeh uasoeuh"},
		{"ignored", "8:anoeuh oeusoeh uasoeuh"},
	}
	finalLabels := labels.Labels{
		{"job", "example"},
		{"instance", "1.2.3.4:80"},
	}

	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		if labels, _ := r.Translate(discoveredLabels, finalLabels); labels == nil {
			b.Fail()
		}
	}
}
