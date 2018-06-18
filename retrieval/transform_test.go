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
	timestamp_pb "github.com/golang/protobuf/ptypes/timestamp"
	promlabels "github.com/prometheus/prometheus/pkg/labels"
	"github.com/prometheus/prometheus/pkg/textparse"
	"github.com/prometheus/prometheus/scrape"
	"github.com/prometheus/tsdb"
	"github.com/prometheus/tsdb/labels"
	metric_pb "google.golang.org/genproto/googleapis/api/metric"
	monitoredres_pb "google.golang.org/genproto/googleapis/api/monitoredres"
	monitoring_pb "google.golang.org/genproto/googleapis/monitoring/v3"
)

// seriesMap implements seriesGetter.
type seriesMap map[uint64]labels.Labels

func (g seriesMap) get(ref uint64) (labels.Labels, bool) {
	ls, ok := g[ref]
	return ls, ok
}

// targetMap implements a TargetGetter that indexes targets by job/instance combination.
// It never returns an error.
type targetMap map[string]*targets.Target

func (g targetMap) Get(ctx context.Context, lset promlabels.Labels) (*targets.Target, error) {
	key := lset.Get("job") + "/" + lset.Get("instance")
	return g[key], nil
}

// metadataMap implements a MetadataGetter for exact matches of job/instance/metric inputs.
type metadataMap map[string]*scrape.MetricMetadata

func (m metadataMap) Get(ctx context.Context, job, instance, metric string) (*scrape.MetricMetadata, error) {
	return m[job+"/"+instance+"/"+metric], nil
}

func TestSampleBuilder(t *testing.T) {
	resourceMaps := []ResourceMap{
		{
			Type:     "resource1",
			LabelMap: map[string]string{"__resource_a": "resource_a", "__resource_b": "resource_b"},
		}, {
			Type:     "resource2",
			LabelMap: map[string]string{"__resource_a": "resource_a"},
		},
	}
	cases := []struct {
		series   seriesGetter
		targets  TargetGetter
		metadata MetadataGetter
		input    []tsdb.RefSample
		result   []*monitoring_pb.TimeSeries
		fail     bool
	}{
		{
			series: seriesMap{
				1: labels.FromStrings("job", "job1", "instance", "instance1", "a", "1", "__name__", "metric1"),
				2: labels.FromStrings("job", "job1", "instance", "instance1", "__name__", "metric2"),
				// Series with more than 10 labels should be dropped. This does not include targets labels
				// and the special metric name label.
				3: labels.FromStrings("job", "job1", "instance", "instance1", "__name__", "labelnum_ok",
					"a", "1", "b", "2", "c", "3", "d", "4", "e", "5", "f", "6", "g", "7", "h", "8", "i", "9", "j", "10"),
				4: labels.FromStrings("job", "job1", "instance", "instance1", "__name__", "labelnum_bad",
					"a", "1", "b", "2", "c", "3", "d", "4", "e", "5", "f", "6", "g", "7", "h", "8", "i", "9", "j", "10", "k", "11"),
			},
			targets: targetMap{
				"job1/instance1": &targets.Target{
					Labels:           promlabels.FromStrings("job", "job1", "instance", "instance1"),
					DiscoveredLabels: promlabels.FromStrings("__resource_a", "resource2_a"),
				},
			},
			metadata: metadataMap{
				"job1/instance1/metric1":      &scrape.MetricMetadata{Type: textparse.MetricTypeGauge},
				"job1/instance1/metric2":      &scrape.MetricMetadata{Type: textparse.MetricTypeCounter},
				"job1/instance1/labelnum_ok":  &scrape.MetricMetadata{Type: textparse.MetricTypeGauge},
				"job1/instance1/labelnum_bad": &scrape.MetricMetadata{Type: textparse.MetricTypeGauge},
			},
			input: []tsdb.RefSample{
				{Ref: 2, T: 2000, V: 5.5},
				{Ref: 1, T: 1000, V: 200},
				{Ref: 3, T: 3000, V: 1},
				{Ref: 4, T: 4000, V: 2},
			},
			result: []*monitoring_pb.TimeSeries{
				{ // 0
					Resource: &monitoredres_pb.MonitoredResource{
						Type:   "resource2",
						Labels: map[string]string{"resource_a": "resource2_a"},
					},
					Metric: &metric_pb.Metric{
						Type:   "external.googleapis.com/prometheus/metric2",
						Labels: map[string]string{},
					},
					MetricKind: metric_pb.MetricDescriptor_CUMULATIVE,
					ValueType:  metric_pb.MetricDescriptor_DOUBLE,
					Points: []*monitoring_pb.Point{{
						Interval: &monitoring_pb.TimeInterval{
							StartTime: &timestamp_pb.Timestamp{Nanos: 1e6}, // TODO(fabxc): update when reset timestamps are implemented.
							EndTime:   &timestamp_pb.Timestamp{Seconds: 2},
						},
						Value: &monitoring_pb.TypedValue{
							Value: &monitoring_pb.TypedValue_DoubleValue{5.5},
						},
					}},
				},
				{ // 1
					Resource: &monitoredres_pb.MonitoredResource{
						Type:   "resource2",
						Labels: map[string]string{"resource_a": "resource2_a"},
					},
					Metric: &metric_pb.Metric{
						Type:   "external.googleapis.com/prometheus/metric1",
						Labels: map[string]string{"a": "1"},
					},
					MetricKind: metric_pb.MetricDescriptor_GAUGE,
					ValueType:  metric_pb.MetricDescriptor_DOUBLE,
					Points: []*monitoring_pb.Point{{
						Interval: &monitoring_pb.TimeInterval{
							EndTime: &timestamp_pb.Timestamp{Seconds: 1},
						},
						Value: &monitoring_pb.TypedValue{
							Value: &monitoring_pb.TypedValue_DoubleValue{200},
						},
					}},
				},
				{ // 2
					Resource: &monitoredres_pb.MonitoredResource{
						Type:   "resource2",
						Labels: map[string]string{"resource_a": "resource2_a"},
					},
					Metric: &metric_pb.Metric{
						Type: "external.googleapis.com/prometheus/labelnum_ok",
						Labels: map[string]string{
							"a": "1", "b": "2", "c": "3", "d": "4", "e": "5", "f": "6", "g": "7", "h": "8", "i": "9", "j": "10",
						},
					},
					MetricKind: metric_pb.MetricDescriptor_GAUGE,
					ValueType:  metric_pb.MetricDescriptor_DOUBLE,
					Points: []*monitoring_pb.Point{{
						Interval: &monitoring_pb.TimeInterval{
							EndTime: &timestamp_pb.Timestamp{Seconds: 3},
						},
						Value: &monitoring_pb.TypedValue{
							Value: &monitoring_pb.TypedValue_DoubleValue{1},
						},
					}},
				},
				nil, // 3: Dropped sample with too many labels.
			},
		},
		// Various cases where we drop series due to absence of additional information.
		{
			targets: targetMap{
				"job1/instance1": &targets.Target{
					Labels:           promlabels.FromStrings("job", "job1", "instance", "instance1"),
					DiscoveredLabels: promlabels.FromStrings("__resource_a", "resource2_a"),
				},
				"job1/instance_noresource": &targets.Target{
					Labels: promlabels.FromStrings("job", "job1", "instance", "instance_noresource"),
				},
			},
			metadata: metadataMap{
				"job1/instance1/metric1": &scrape.MetricMetadata{Type: textparse.MetricTypeGauge},
			},
			series: seriesMap{
				1: labels.FromStrings("job", "job1", "instance", "instance_notfound", "__name__", "metric1"),
				2: labels.FromStrings("job", "job1", "instance", "instance1", "__name__", "metric_notfound"),
				3: labels.FromStrings("job", "job1", "instance", "instance_noresource", "__name__", "metric1"),
			},
			input: []tsdb.RefSample{
				{Ref: 1, T: 1000, V: 1},
				{Ref: 2, T: 2000, V: 2},
				{Ref: 3, T: 3000, V: 3},
			},
			result: []*monitoring_pb.TimeSeries{nil, nil, nil},
		},
	}
	for i, c := range cases {
		t.Logf("Test case %d", i)

		var s *monitoring_pb.TimeSeries
		var err error
		var result []*monitoring_pb.TimeSeries

		b := &sampleBuilder{
			resourceMaps: resourceMaps,
			series:       c.series,
			targets:      c.targets,
			metadata:     c.metadata,
		}
		for k := 0; len(c.input) > 0; k++ {
			s, c.input, err = b.next(context.Background(), c.input)
			if err != nil {
				break
			}
			result = append(result, s)
		}
		if err == nil && c.fail {
			t.Fatal("expected error but got none")
		}
		if err != nil && !c.fail {
			t.Fatalf("unexpected error: %s", err)
		}
		if len(result) != len(c.result) {
			t.Fatalf("mismatching count %d of received samples, want %d", len(result), len(c.result))
		}
		for k, res := range result {
			if !reflect.DeepEqual(res, c.result[k]) {
				t.Fatalf("unexpected sample %v, want %v", res, c.result[k])
			}
		}

	}
}
