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
	"github.com/go-kit/kit/log"
	timestamp_pb "github.com/golang/protobuf/ptypes/timestamp"
	promlabels "github.com/prometheus/prometheus/pkg/labels"
	"github.com/prometheus/prometheus/pkg/textparse"
	"github.com/prometheus/prometheus/scrape"
	"github.com/prometheus/tsdb"
	"github.com/prometheus/tsdb/labels"
	distribution_pb "google.golang.org/genproto/googleapis/api/distribution"
	metric_pb "google.golang.org/genproto/googleapis/api/metric"
	monitoredres_pb "google.golang.org/genproto/googleapis/api/monitoredres"
	monitoring_pb "google.golang.org/genproto/googleapis/monitoring/v3"
)

// seriesMap implements seriesGetter.
type seriesMap map[uint64]labels.Labels

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
			Type: "resource1",
			LabelMap: map[string]labelTranslation{
				"__resource_a": constValue("resource_a"),
				"__resource_b": constValue("resource_b"),
			},
		}, {
			Type: "resource2",
			LabelMap: map[string]labelTranslation{
				"__resource_a": constValue("resource_a"),
			},
		},
	}
	cases := []struct {
		series       seriesMap
		targets      TargetGetter
		metadata     MetadataGetter
		metricPrefix string
		input        []tsdb.RefSample
		result       []*monitoring_pb.TimeSeries
		fail         bool
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
				"job1/instance1/metric1":      &scrape.MetricMetadata{Type: textparse.MetricTypeGauge, Metric: "metric1"},
				"job1/instance1/metric2":      &scrape.MetricMetadata{Type: textparse.MetricTypeCounter, Metric: "metric2"},
				"job1/instance1/labelnum_ok":  &scrape.MetricMetadata{Type: textparse.MetricTypeUnknown, Metric: "labelnum_ok"},
				"job1/instance1/labelnum_bad": &scrape.MetricMetadata{Type: textparse.MetricTypeGauge, Metric: "labelnum_bad"},
			},
			input: []tsdb.RefSample{
				{Ref: 2, T: 2000, V: 5.5},
				{Ref: 2, T: 3000, V: 8},
				{Ref: 2, T: 4000, V: 9},
				{Ref: 2, T: 5000, V: 3},
				{Ref: 1, T: 1000, V: 200},
				{Ref: 3, T: 3000, V: 1},
				{Ref: 4, T: 4000, V: 2},
			},
			result: []*monitoring_pb.TimeSeries{
				nil, // Skipped by reset timestamp handling.
				{ // 1
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
							StartTime: &timestamp_pb.Timestamp{Seconds: 2},
							EndTime:   &timestamp_pb.Timestamp{Seconds: 3},
						},
						Value: &monitoring_pb.TypedValue{
							Value: &monitoring_pb.TypedValue_DoubleValue{2.5},
						},
					}},
				},
				{ // 2
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
							StartTime: &timestamp_pb.Timestamp{Seconds: 2},
							EndTime:   &timestamp_pb.Timestamp{Seconds: 4},
						},
						Value: &monitoring_pb.TypedValue{
							Value: &monitoring_pb.TypedValue_DoubleValue{3.5},
						},
					}},
				},
				{ // 3
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
							StartTime: &timestamp_pb.Timestamp{Seconds: 4, Nanos: 1e9 - 1e6},
							EndTime:   &timestamp_pb.Timestamp{Seconds: 5},
						},
						Value: &monitoring_pb.TypedValue{
							Value: &monitoring_pb.TypedValue_DoubleValue{3},
						},
					}},
				},
				{ // 4
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
				{ // 5
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
				nil, // 6: Dropped sample with too many labels.
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
				"job1/instance1/metric1": &scrape.MetricMetadata{Type: textparse.MetricTypeGauge, Metric: "metric1"},
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
		// Summary metrics.
		{
			targets: targetMap{
				"job1/instance1": &targets.Target{
					Labels:           promlabels.FromStrings("job", "job1", "instance", "instance1"),
					DiscoveredLabels: promlabels.FromStrings("__resource_a", "resource2_a"),
				},
			},
			metadata: metadataMap{
				"job1/instance1/metric1": &scrape.MetricMetadata{Type: textparse.MetricTypeSummary, Metric: "metric1"},
			},
			series: seriesMap{
				1: labels.FromStrings("job", "job1", "instance", "instance1", "__name__", "metric1_sum"),
				2: labels.FromStrings("job", "job1", "instance", "instance1", "__name__", "metric1", "quantile", "0.5"),
				3: labels.FromStrings("job", "job1", "instance", "instance1", "__name__", "metric1_count"),
				4: labels.FromStrings("job", "job1", "instance", "instance1", "__name__", "metric1", "quantile", "0.9"),
			},
			input: []tsdb.RefSample{
				{Ref: 1, T: 1000, V: 1},
				{Ref: 1, T: 1500, V: 1},
				{Ref: 2, T: 2000, V: 2},
				{Ref: 3, T: 3000, V: 3},
				{Ref: 3, T: 3500, V: 4},
				{Ref: 4, T: 4000, V: 4},
			},
			result: []*monitoring_pb.TimeSeries{
				nil, // 0: dropped by reset handling.
				{ // 1
					Resource: &monitoredres_pb.MonitoredResource{
						Type:   "resource2",
						Labels: map[string]string{"resource_a": "resource2_a"},
					},
					Metric: &metric_pb.Metric{
						Type:   "external.googleapis.com/prometheus/metric1_sum",
						Labels: map[string]string{},
					},
					MetricKind: metric_pb.MetricDescriptor_CUMULATIVE,
					ValueType:  metric_pb.MetricDescriptor_DOUBLE,
					Points: []*monitoring_pb.Point{{
						Interval: &monitoring_pb.TimeInterval{
							StartTime: &timestamp_pb.Timestamp{Seconds: 1},
							EndTime:   &timestamp_pb.Timestamp{Seconds: 1, Nanos: 5e8},
						},
						Value: &monitoring_pb.TypedValue{
							Value: &monitoring_pb.TypedValue_DoubleValue{0},
						},
					}},
				},
				{ // 2
					Resource: &monitoredres_pb.MonitoredResource{
						Type:   "resource2",
						Labels: map[string]string{"resource_a": "resource2_a"},
					},
					Metric: &metric_pb.Metric{
						Type:   "external.googleapis.com/prometheus/metric1",
						Labels: map[string]string{"quantile": "0.5"},
					},
					MetricKind: metric_pb.MetricDescriptor_GAUGE,
					ValueType:  metric_pb.MetricDescriptor_DOUBLE,
					Points: []*monitoring_pb.Point{{
						Interval: &monitoring_pb.TimeInterval{
							EndTime: &timestamp_pb.Timestamp{Seconds: 2},
						},
						Value: &monitoring_pb.TypedValue{
							Value: &monitoring_pb.TypedValue_DoubleValue{2},
						},
					}},
				},
				nil, // 3: dropped by reset handling.
				{ // 4
					Resource: &monitoredres_pb.MonitoredResource{
						Type:   "resource2",
						Labels: map[string]string{"resource_a": "resource2_a"},
					},
					Metric: &metric_pb.Metric{
						Type:   "external.googleapis.com/prometheus/metric1_count",
						Labels: map[string]string{},
					},
					MetricKind: metric_pb.MetricDescriptor_CUMULATIVE,
					ValueType:  metric_pb.MetricDescriptor_INT64,
					Points: []*monitoring_pb.Point{{
						Interval: &monitoring_pb.TimeInterval{
							StartTime: &timestamp_pb.Timestamp{Seconds: 3},
							EndTime:   &timestamp_pb.Timestamp{Seconds: 3, Nanos: 5e8},
						},
						Value: &monitoring_pb.TypedValue{
							Value: &monitoring_pb.TypedValue_Int64Value{1},
						},
					}},
				},
				{ // 5
					Resource: &monitoredres_pb.MonitoredResource{
						Type:   "resource2",
						Labels: map[string]string{"resource_a": "resource2_a"},
					},
					Metric: &metric_pb.Metric{
						Type:   "external.googleapis.com/prometheus/metric1",
						Labels: map[string]string{"quantile": "0.9"},
					},
					MetricKind: metric_pb.MetricDescriptor_GAUGE,
					ValueType:  metric_pb.MetricDescriptor_DOUBLE,
					Points: []*monitoring_pb.Point{{
						Interval: &monitoring_pb.TimeInterval{
							EndTime: &timestamp_pb.Timestamp{Seconds: 4},
						},
						Value: &monitoring_pb.TypedValue{
							Value: &monitoring_pb.TypedValue_DoubleValue{4},
						},
					}},
				},
			},
		},
		// Histogram.
		{
			targets: targetMap{
				"job1/instance1": &targets.Target{
					Labels:           promlabels.FromStrings("job", "job1", "instance", "instance1"),
					DiscoveredLabels: promlabels.FromStrings("__resource_a", "resource2_a"),
				},
			},
			metadata: metadataMap{
				"job1/instance1/metric1":         &scrape.MetricMetadata{Type: textparse.MetricTypeHistogram, Metric: "metric1"},
				"job1/instance1/metric1_a_count": &scrape.MetricMetadata{Type: textparse.MetricTypeGauge, Metric: "metric1_a_count"},
			},
			series: seriesMap{
				1: labels.FromStrings("job", "job1", "instance", "instance1", "__name__", "metric1_sum"),
				2: labels.FromStrings("job", "job1", "instance", "instance1", "__name__", "metric1_count"),
				3: labels.FromStrings("job", "job1", "instance", "instance1", "__name__", "metric1_bucket", "le", "0.1"),
				4: labels.FromStrings("job", "job1", "instance", "instance1", "__name__", "metric1_bucket", "le", "0.5"),
				5: labels.FromStrings("job", "job1", "instance", "instance1", "__name__", "metric1_bucket", "le", "1"),
				6: labels.FromStrings("job", "job1", "instance", "instance1", "__name__", "metric1_bucket", "le", "2.5"),
				7: labels.FromStrings("job", "job1", "instance", "instance1", "__name__", "metric1_bucket", "le", "+Inf"),
				// Add another series that only deviates by having an extra label. We must properly detect a new histogram.
				// This is an discouraged but possible case of metric labeling.
				8: labels.FromStrings("job", "job1", "instance", "instance1", "a", "b", "__name__", "metric1_sum"),
				9: labels.FromStrings("job", "job1", "instance", "instance1", "a", "b", "__name__", "metric1_count"),
				// Series that triggers more edge cases.
				10: labels.FromStrings("job", "job1", "instance", "instance1", "a", "b", "__name__", "metric1_a_count"),
			},
			input: []tsdb.RefSample{
				// Mix up order of the series to test bucket sorting.
				// First sample set, should be skipped by reset handling.
				{Ref: 3, T: 1000, V: 2},    // 0.1
				{Ref: 5, T: 1000, V: 6},    // 1
				{Ref: 6, T: 1000, V: 8},    // 2.5
				{Ref: 7, T: 1000, V: 10},   // inf
				{Ref: 1, T: 1000, V: 55.1}, // sum
				{Ref: 4, T: 1000, V: 5},    // 0.5
				{Ref: 2, T: 1000, V: 10},   // count
				// Second sample set should actually be emitted.
				{Ref: 2, T: 2000, V: 21},    // count
				{Ref: 3, T: 2000, V: 4},     // 0.1
				{Ref: 6, T: 2000, V: 15},    // 2.5
				{Ref: 5, T: 2000, V: 11},    // 1
				{Ref: 1, T: 2000, V: 123.4}, // sum
				{Ref: 7, T: 2000, V: 21},    // inf
				{Ref: 4, T: 2000, V: 9},     // 0.5
				// New histogram without actual buckets – should still work.
				{Ref: 8, T: 1000, V: 100},
				{Ref: 9, T: 1000, V: 10},
				{Ref: 8, T: 2000, V: 115},
				{Ref: 9, T: 2000, V: 13},
				// New metric that actually matches the base name but the suffix is more more than a valid histogram suffix.
				{Ref: 10, T: 1000, V: 3},
			},
			result: []*monitoring_pb.TimeSeries{
				nil, // 0: skipped by reset handling.
				{ // 1
					Resource: &monitoredres_pb.MonitoredResource{
						Type:   "resource2",
						Labels: map[string]string{"resource_a": "resource2_a"},
					},
					Metric: &metric_pb.Metric{
						Type:   "external.googleapis.com/prometheus/metric1",
						Labels: map[string]string{},
					},
					MetricKind: metric_pb.MetricDescriptor_CUMULATIVE,
					ValueType:  metric_pb.MetricDescriptor_DISTRIBUTION,
					Points: []*monitoring_pb.Point{{
						Interval: &monitoring_pb.TimeInterval{
							StartTime: &timestamp_pb.Timestamp{Seconds: 1},
							EndTime:   &timestamp_pb.Timestamp{Seconds: 2},
						},
						Value: &monitoring_pb.TypedValue{
							Value: &monitoring_pb.TypedValue_DistributionValue{
								&distribution_pb.Distribution{
									Count:                 11,
									Mean:                  6.20909090909091,
									SumOfSquaredDeviation: 270.301590909091,
									BucketOptions: &distribution_pb.Distribution_BucketOptions{
										Options: &distribution_pb.Distribution_BucketOptions_ExplicitBuckets{
											ExplicitBuckets: &distribution_pb.Distribution_BucketOptions_Explicit{
												Bounds: []float64{0.1, 0.5, 1, 2.5},
											},
										},
									},
									BucketCounts: []int64{2, 2, 1, 2, 4},
								},
							},
						},
					}},
				},
				nil, // 2: skipped by reset handling
				{ // 3
					Resource: &monitoredres_pb.MonitoredResource{
						Type:   "resource2",
						Labels: map[string]string{"resource_a": "resource2_a"},
					},
					Metric: &metric_pb.Metric{
						Type:   "external.googleapis.com/prometheus/metric1",
						Labels: map[string]string{"a": "b"},
					},
					MetricKind: metric_pb.MetricDescriptor_CUMULATIVE,
					ValueType:  metric_pb.MetricDescriptor_DISTRIBUTION,
					Points: []*monitoring_pb.Point{{
						Interval: &monitoring_pb.TimeInterval{
							StartTime: &timestamp_pb.Timestamp{Seconds: 1},
							EndTime:   &timestamp_pb.Timestamp{Seconds: 2},
						},
						Value: &monitoring_pb.TypedValue{
							Value: &monitoring_pb.TypedValue_DistributionValue{
								&distribution_pb.Distribution{
									Count:                 3,
									Mean:                  5,
									SumOfSquaredDeviation: 0,
									BucketOptions: &distribution_pb.Distribution_BucketOptions{
										Options: &distribution_pb.Distribution_BucketOptions_ExplicitBuckets{
											ExplicitBuckets: &distribution_pb.Distribution_BucketOptions_Explicit{
												Bounds: []float64{},
											},
										},
									},
									BucketCounts: []int64{},
								},
							},
						},
					}},
				},
				{ // 4
					Resource: &monitoredres_pb.MonitoredResource{
						Type:   "resource2",
						Labels: map[string]string{"resource_a": "resource2_a"},
					},
					Metric: &metric_pb.Metric{
						Type:   "external.googleapis.com/prometheus/metric1_a_count",
						Labels: map[string]string{"a": "b"},
					},
					MetricKind: metric_pb.MetricDescriptor_GAUGE,
					ValueType:  metric_pb.MetricDescriptor_DOUBLE,
					Points: []*monitoring_pb.Point{{
						Interval: &monitoring_pb.TimeInterval{
							EndTime: &timestamp_pb.Timestamp{Seconds: 1},
						},
						Value: &monitoring_pb.TypedValue{
							Value: &monitoring_pb.TypedValue_DoubleValue{3},
						},
					}},
				},
			},
		},
		// Interval overlap handling.
		{
			series: seriesMap{
				1: labels.FromStrings("job", "job1", "instance", "instance1", "__name__", "metric1"),
				2: labels.FromStrings("job", "job1", "instance", "instance2", "__name__", "metric1"),
			},
			// Both instances map to the same monitored resource and will thus produce the same series.
			targets: targetMap{
				"job1/instance1": &targets.Target{
					Labels:           promlabels.FromStrings("job", "job1", "instance", "instance1"),
					DiscoveredLabels: promlabels.FromStrings("__resource_a", "resource2_a"),
				},
				"job1/instance2": &targets.Target{
					Labels:           promlabels.FromStrings("job", "job1", "instance", "instance2"),
					DiscoveredLabels: promlabels.FromStrings("__resource_a", "resource2_a"),
				},
			},
			metadata: metadataMap{
				"job1/instance1/metric1": &scrape.MetricMetadata{Type: textparse.MetricTypeCounter, Metric: "metric1"},
				"job1/instance2/metric1": &scrape.MetricMetadata{Type: textparse.MetricTypeCounter, Metric: "metric1"},
			},
			input: []tsdb.RefSample{
				// First sample for both series will define the reset timestamp.
				{Ref: 1, T: 1000, V: 4},
				{Ref: 2, T: 1500, V: 5},
				// The sample for series 2 must be rejected.
				{Ref: 1, T: 2000, V: 9},
				{Ref: 2, T: 2500, V: 11},
				// Both series get reset but the 2nd one is detected first.
				// The emitted samples should flip over.
				{Ref: 2, T: 3500, V: 3},
				{Ref: 1, T: 3000, V: 2},
			},
			result: []*monitoring_pb.TimeSeries{
				nil, // Skipped by reset timestamp handling.
				nil, // Skipped by reset timestamp handling.
				{
					Resource: &monitoredres_pb.MonitoredResource{
						Type:   "resource2",
						Labels: map[string]string{"resource_a": "resource2_a"},
					},
					Metric: &metric_pb.Metric{
						Type:   "external.googleapis.com/prometheus/metric1",
						Labels: map[string]string{},
					},
					MetricKind: metric_pb.MetricDescriptor_CUMULATIVE,
					ValueType:  metric_pb.MetricDescriptor_DOUBLE,
					Points: []*monitoring_pb.Point{{
						Interval: &monitoring_pb.TimeInterval{
							StartTime: &timestamp_pb.Timestamp{Seconds: 1},
							EndTime:   &timestamp_pb.Timestamp{Seconds: 2},
						},
						Value: &monitoring_pb.TypedValue{
							Value: &monitoring_pb.TypedValue_DoubleValue{5},
						},
					}},
				},
				nil,
				{
					Resource: &monitoredres_pb.MonitoredResource{
						Type:   "resource2",
						Labels: map[string]string{"resource_a": "resource2_a"},
					},
					Metric: &metric_pb.Metric{
						Type:   "external.googleapis.com/prometheus/metric1",
						Labels: map[string]string{},
					},
					MetricKind: metric_pb.MetricDescriptor_CUMULATIVE,
					ValueType:  metric_pb.MetricDescriptor_DOUBLE,
					Points: []*monitoring_pb.Point{{
						Interval: &monitoring_pb.TimeInterval{
							StartTime: &timestamp_pb.Timestamp{Seconds: 3, Nanos: 5e8 - 1e6},
							EndTime:   &timestamp_pb.Timestamp{Seconds: 3, Nanos: 5e8},
						},
						Value: &monitoring_pb.TypedValue{
							Value: &monitoring_pb.TypedValue_DoubleValue{3},
						},
					}},
				},
				nil,
			},
		},
		// Customized metric prefix.
		{
			series: seriesMap{
				1: labels.FromStrings("job", "job1", "instance", "instance1", "a", "1", "__name__", "metric1"),
			},
			targets: targetMap{
				"job1/instance1": &targets.Target{
					Labels:           promlabels.FromStrings("job", "job1", "instance", "instance1"),
					DiscoveredLabels: promlabels.FromStrings("__resource_a", "resource2_a"),
				},
			},
			metadata: metadataMap{
				"job1/instance1/metric1": &scrape.MetricMetadata{Type: textparse.MetricTypeGauge, Metric: "metric1"},
			},
			metricPrefix: "test.googleapis.com",
			input: []tsdb.RefSample{
				{Ref: 1, T: 1000, V: 200},
			},
			result: []*monitoring_pb.TimeSeries{
				{
					Resource: &monitoredres_pb.MonitoredResource{
						Type:   "resource2",
						Labels: map[string]string{"resource_a": "resource2_a"},
					},
					Metric: &metric_pb.Metric{
						Type:   "test.googleapis.com/metric1",
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
			},
		},
		// Any counter metric with the _total suffix should be treated as normal if metadata
		// can be found for the original metric name.
		{
			series: seriesMap{
				1: labels.FromStrings("job", "job1", "instance", "instance1", "a", "1", "__name__", "metric1_total"),
			},
			targets: targetMap{
				"job1/instance1": &targets.Target{
					Labels:           promlabels.FromStrings("job", "job1", "instance", "instance1"),
					DiscoveredLabels: promlabels.FromStrings("__resource_a", "resource2_a"),
				},
			},
			metadata: metadataMap{
				"job1/instance1/metric1_total": &scrape.MetricMetadata{Type: textparse.MetricTypeCounter, Metric: "metric1_total"},
			},
			metricPrefix: "test.googleapis.com",
			input: []tsdb.RefSample{
				{Ref: 1, T: 2000, V: 5.5},
				{Ref: 1, T: 3000, V: 8},
			},
			result: []*monitoring_pb.TimeSeries{
				nil, // Skipped by reset timestamp handling.
				{
					Resource: &monitoredres_pb.MonitoredResource{
						Type:   "resource2",
						Labels: map[string]string{"resource_a": "resource2_a"},
					},
					Metric: &metric_pb.Metric{
						Type:   "test.googleapis.com/metric1_total",
						Labels: map[string]string{"a": "1"},
					},
					MetricKind: metric_pb.MetricDescriptor_CUMULATIVE,
					ValueType:  metric_pb.MetricDescriptor_DOUBLE,
					Points: []*monitoring_pb.Point{{
						Interval: &monitoring_pb.TimeInterval{
							StartTime: &timestamp_pb.Timestamp{Seconds: 2},
							EndTime:   &timestamp_pb.Timestamp{Seconds: 3},
						},
						Value: &monitoring_pb.TypedValue{
							Value: &monitoring_pb.TypedValue_DoubleValue{2.5},
						},
					}},
				},
			},
		},
		// Any counter metric with the _total suffix should fail over to the metadata for
		// the metric with the _total suffix removed while reporting the metric with the
		// _total suffix removed in the metric name as well.
		{
			series: seriesMap{
				1: labels.FromStrings("job", "job1", "instance", "instance1", "a", "1", "__name__", "metric1_total"),
			},
			targets: targetMap{
				"job1/instance1": &targets.Target{
					Labels:           promlabels.FromStrings("job", "job1", "instance", "instance1"),
					DiscoveredLabels: promlabels.FromStrings("__resource_a", "resource2_a"),
				},
			},
			metadata: metadataMap{
				"job1/instance1/metric1": &scrape.MetricMetadata{Type: textparse.MetricTypeCounter, Metric: "metric1"},
			},
			metricPrefix: "test.googleapis.com",
			input: []tsdb.RefSample{
				{Ref: 1, T: 2000, V: 5.5},
				{Ref: 1, T: 3000, V: 8},
			},
			result: []*monitoring_pb.TimeSeries{
				nil, // Skipped by reset timestamp handling.
				{
					Resource: &monitoredres_pb.MonitoredResource{
						Type:   "resource2",
						Labels: map[string]string{"resource_a": "resource2_a"},
					},
					Metric: &metric_pb.Metric{
						Type:   "test.googleapis.com/metric1",
						Labels: map[string]string{"a": "1"},
					},
					MetricKind: metric_pb.MetricDescriptor_CUMULATIVE,
					ValueType:  metric_pb.MetricDescriptor_DOUBLE,
					Points: []*monitoring_pb.Point{{
						Interval: &monitoring_pb.TimeInterval{
							StartTime: &timestamp_pb.Timestamp{Seconds: 2},
							EndTime:   &timestamp_pb.Timestamp{Seconds: 3},
						},
						Value: &monitoring_pb.TypedValue{
							Value: &monitoring_pb.TypedValue_DoubleValue{2.5},
						},
					}},
				},
			},
		},
		// Any non-counter metric with the _total suffix should fail over to the metadata
		// for the metric with the _total suffix removed while reporting the metric with
		// the original name.
		{
			series: seriesMap{
				1: labels.FromStrings("job", "job1", "instance", "instance1", "a", "1", "__name__", "metric1_total"),
			},
			targets: targetMap{
				"job1/instance1": &targets.Target{
					Labels:           promlabels.FromStrings("job", "job1", "instance", "instance1"),
					DiscoveredLabels: promlabels.FromStrings("__resource_a", "resource2_a"),
				},
			},
			metadata: metadataMap{
				"job1/instance1/metric1": &scrape.MetricMetadata{Type: textparse.MetricTypeGauge, Metric: "metric1"},
			},
			metricPrefix: "test.googleapis.com",
			input: []tsdb.RefSample{
				{Ref: 1, T: 3000, V: 8},
			},
			result: []*monitoring_pb.TimeSeries{
				{
					Resource: &monitoredres_pb.MonitoredResource{
						Type:   "resource2",
						Labels: map[string]string{"resource_a": "resource2_a"},
					},
					Metric: &metric_pb.Metric{
						Type:   "test.googleapis.com/metric1_total",
						Labels: map[string]string{"a": "1"},
					},
					MetricKind: metric_pb.MetricDescriptor_GAUGE,
					ValueType:  metric_pb.MetricDescriptor_DOUBLE,
					Points: []*monitoring_pb.Point{{
						Interval: &monitoring_pb.TimeInterval{
							EndTime: &timestamp_pb.Timestamp{Seconds: 3},
						},
						Value: &monitoring_pb.TypedValue{
							Value: &monitoring_pb.TypedValue_DoubleValue{8},
						},
					}},
				},
			},
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	for i, c := range cases {
		t.Logf("Test case %d", i)

		var s *monitoring_pb.TimeSeries
		var h uint64
		var err error
		var result []*monitoring_pb.TimeSeries
		var hashes []uint64

		aggr, _ := NewCounterAggregator(log.NewNopLogger(), new(CounterAggregatorConfig))
		series := newSeriesCache(nil, "", nil, nil, c.targets, c.metadata, resourceMaps, c.metricPrefix, false, aggr)
		for ref, s := range c.series {
			series.set(ctx, ref, s, 0)
		}

		b := &sampleBuilder{series: series}

		for k := 0; len(c.input) > 0; k++ {
			s, h, c.input, err = b.next(context.Background(), c.input)
			if err != nil {
				break
			}
			result = append(result, s)
			hashes = append(hashes, h)
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
				t.Logf("gotres %v", result)
				t.Logf("expres %v", c.result)
				t.Fatalf("unexpected sample %d: got\n\t%v\nwant\n\t%v", k, res, c.result[k])
			}
			expectedHash := uint64(0)
			if c.result[k] != nil {
				expectedHash = hashSeries(c.result[k])
			}
			if hashes[k] != expectedHash {
				t.Fatalf("unexpected hash %v; want %v", hashes[k], expectedHash)
			}
		}
	}
}
