/*
Copyright 2018 Google Inc.
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
	"context"
	"reflect"
	"testing"

	"github.com/Stackdriver/stackdriver-prometheus-sidecar/targets"
	promlabels "github.com/prometheus/prometheus/pkg/labels"
	metric_pb "google.golang.org/genproto/googleapis/api/metric"
	monitoredres_pb "google.golang.org/genproto/googleapis/api/monitoredres"
	monitoring_pb "google.golang.org/genproto/googleapis/monitoring/v3"
)

func TestTargetsWithDiscoveredLabels(t *testing.T) {
	tm := targetMap{
		"/": &targets.Target{DiscoveredLabels: promlabels.FromStrings("b", "2")},
	}

	wrapped := TargetsWithDiscoveredLabels(tm, promlabels.FromStrings("a", "1", "c", "3"))

	target, err := wrapped.Get(context.Background(), promlabels.FromStrings("b", "2"))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(target.DiscoveredLabels, promlabels.FromStrings("a", "1", "b", "2", "c", "3")) {
		t.Fatalf("unexpected discovered labels %s", target.DiscoveredLabels)
	}
}

func TestHashSeries(t *testing.T) {
	a := &monitoring_pb.TimeSeries{
		Resource: &monitoredres_pb.MonitoredResource{
			Type:   "rtype1",
			Labels: map[string]string{"l1": "v1", "l2": "v2"},
		},
		Metric: &metric_pb.Metric{
			Type:   "mtype1",
			Labels: map[string]string{"l3": "v3", "l4": "v4"},
		},
	}
	// Hash a many times and ensure the hash doesn't change. This checks that we don't produce different
	// hashes by unordered map iteration.
	hash := hashSeries(a)
	for i := 0; i < 1000; i++ {
		if hashSeries(a) != hash {
			t.Fatalf("hash changed for same series")
		}
	}
	for _, b := range []*monitoring_pb.TimeSeries{
		{
			Resource: &monitoredres_pb.MonitoredResource{
				Type:   "rtype1",
				Labels: map[string]string{"l1": "v1", "l2": "v2"},
			},
			Metric: &metric_pb.Metric{
				Type:   "mtype2",
				Labels: map[string]string{"l3": "v3", "l4": "v4"},
			},
		}, {
			Resource: &monitoredres_pb.MonitoredResource{
				Type:   "rtype2",
				Labels: map[string]string{"l1": "v1", "l2": "v2"},
			},
			Metric: &metric_pb.Metric{
				Type:   "mtype1",
				Labels: map[string]string{"l3": "v3", "l4": "v4"},
			},
		}, {
			Resource: &monitoredres_pb.MonitoredResource{
				Type:   "rtype1",
				Labels: map[string]string{"l1": "v1", "l2": "v2"},
			},
			Metric: &metric_pb.Metric{
				Type:   "mtype1",
				Labels: map[string]string{"l3": "v3", "l4": "v4-"},
			},
		}, {
			Resource: &monitoredres_pb.MonitoredResource{
				Type:   "rtype1",
				Labels: map[string]string{"l1": "v1-", "l2": "v2"},
			},
			Metric: &metric_pb.Metric{
				Type:   "mtype1",
				Labels: map[string]string{"l3": "v3", "l4": "v4"},
			},
		},
	} {
		if hashSeries(b) == hash {
			t.Fatalf("hash for different series did not change")
		}
	}

}
