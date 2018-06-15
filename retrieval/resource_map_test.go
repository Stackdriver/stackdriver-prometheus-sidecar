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

	"github.com/prometheus/prometheus/pkg/labels"
)

func TestTranslate(t *testing.T) {
	r := ResourceMap{
		Type: "my_type",
		LabelMap: map[string]string{
			"__target1": "sdt1",
			"__target2": "sdt2",
		},
	}
	// This target is missing label "__target1".
	noMatchTarget := labels.Labels{
		{"ignored", "x"},
		{"__target2", "y"},
	}
	matchTarget := labels.Labels{
		{"ignored", "x"},
		{"__target2", "y"},
		{"__target1", "z"},
	}
	if labels := r.Translate(noMatchTarget); labels != nil {
		t.Errorf("Expected no match, matched %v", labels)
	}

	expectedLabels := map[string]string{
		"sdt1": "z",
		"sdt2": "y",
	}
	if labels := r.Translate(matchTarget); labels == nil {
		t.Errorf("Expected %v, actual nil", expectedLabels)
	} else if !reflect.DeepEqual(labels, expectedLabels) {
		t.Errorf("Expected %v, actual %v", expectedLabels, labels)
	}
}

func BenchmarkTranslate(b *testing.B) {
	r := ResourceMap{
		Type: "gke_container",
		LabelMap: map[string]string{
			ProjectIDLabel:                   "project_id",
			"_kubernetes_location":           "zone",
			"_kubernetes_cluster_name":       "cluster_name",
			"_kubernetes_namespace":          "namespace_id",
			"_kubernetes_pod_name":           "pod_id",
			"_kubernetes_pod_node_name":      "instance_id",
			"_kubernetes_pod_container_name": "container_name",
		},
	}
	targetLabels := labels.Labels{
		{ProjectIDLabel, "1:anoeuh oeusoeh uasoeuh"},
		{"_kubernetes_location", "2:anoeuh oeusoeh uasoeuh"},
		{"_kubernetes_cluster_name", "3:anoeuh oeusoeh uasoeuh"},
		{"_kubernetes_namespace", "4:anoeuh oeusoeh uasoeuh"},
		{"_kubernetes_pod_name", "5:anoeuh oeusoeh uasoeuh"},
		{"_kubernetes_pod_node_name", "6:anoeuh oeusoeh uasoeuh"},
		{"_kubernetes_pod_container_name", "7:anoeuh oeusoeh uasoeuh"},
		{"ignored", "8:anoeuh oeusoeh uasoeuh"},
	}

	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		if labels := r.Translate(targetLabels); labels == nil {
			b.Fail()
		}
	}
}
