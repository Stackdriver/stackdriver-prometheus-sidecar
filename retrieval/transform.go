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
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Stackdriver/stackdriver-prometheus-sidecar/targets"
	timestamp_pb "github.com/golang/protobuf/ptypes/timestamp"
	"github.com/pkg/errors"
	"github.com/prometheus/prometheus/pkg/labels"
	"github.com/prometheus/prometheus/pkg/textparse"
	"github.com/prometheus/tsdb"
	tsdbLabels "github.com/prometheus/tsdb/labels"
	distribution_pb "google.golang.org/genproto/googleapis/api/distribution"
	metric_pb "google.golang.org/genproto/googleapis/api/metric"
	monitoredres_pb "google.golang.org/genproto/googleapis/api/monitoredres"
	monitoring_pb "google.golang.org/genproto/googleapis/monitoring/v3"
)

type sampleBuilder struct {
	resourceMaps []ResourceMap
	series       seriesGetter
	targets      TargetGetter
	metadata     MetadataGetter
}

// next extracts the next sample from the TSDB input sample list and returns
// the remainder of the input.
func (b *sampleBuilder) next(ctx context.Context, samples []tsdb.RefSample) (*monitoring_pb.TimeSeries, []tsdb.RefSample, error) {
	sample := samples[0]
	lset, ok := b.series.get(sample.Ref)
	if !ok {
		return nil, samples[1:], errors.Errorf("No series matched by ref %d", sample.Ref)
	}
	// Use the first available sample to probe for the target, its applicable resource, and the
	// series metadata.
	// They will be used subsequently for all other Prometheus series that map to the same complex
	// Stackdriver series.
	// If either of those pieces of data is missing, the series will be skipped.
	target, err := b.targets.Get(ctx, pkgLabels(lset))
	if err != nil {
		return nil, samples, errors.Wrap(err, "retrieving target failed")
	}
	if target == nil {
		// TODO(fabxc): increment a metric.
		return nil, samples[1:], nil
	}
	// Remove target labels and __name__ label.
	finalLabels := targets.DropTargetLabels(pkgLabels(lset), target.Labels)
	for i, l := range finalLabels {
		if l.Name == "__name__" {
			finalLabels = append(finalLabels[:i], finalLabels[i+1:]...)
			break
		}
	}
	// Drop series with too many labels.
	if len(finalLabels) > maxLabelCount {
		// TODO(fabxc): increment a metric
		return nil, samples[1:], nil
	}

	resource, ok := b.getResource(target.DiscoveredLabels)
	if !ok {
		// TODO(fabxc): increment a metric
		return nil, samples[1:], nil
	}
	var (
		metricName     = lset.Get("__name__")
		baseMetricName string // metric name stripped by potential suffixes.
		suffix         string
	)
	metadata, err := b.metadata.Get(ctx, lset.Get("job"), lset.Get("instance"), metricName)
	if err != nil {
		return nil, samples, errors.Wrap(err, "get metadata")
	}
	if metadata == nil {
		// The full name didn't turn anything up. Check again in case it's a summary or histogram without
		// the metric name suffix.
		var ok bool
		if baseMetricName, suffix, ok = stripComplexMetricSuffix(metricName); ok {
			metadata, err = b.metadata.Get(ctx, lset.Get("job"), lset.Get("instance"), baseMetricName)
			if err != nil {
				return nil, samples, errors.Wrap(err, "get metadata")
			}
		}
		if metadata == nil {
			// TODO(fabxc): increment a metric.
			return nil, samples[1:], nil
		}
	}
	// Handle label modifications for histograms early so we don't build the label map twice.
	if metadata.Type == textparse.MetricTypeHistogram {
		for i, l := range finalLabels {
			if l.Name == "le" {
				finalLabels = append(finalLabels[:i], finalLabels[i+1:]...)
				break
			}
		}
	}
	point := &monitoring_pb.Point{
		Interval: &monitoring_pb.TimeInterval{
			EndTime: getTimestamp(sample.T),
		},
	}
	res := &monitoring_pb.TimeSeries{
		Metric: &metric_pb.Metric{
			Type:   getMetricType(metricName),
			Labels: finalLabels.Map(),
		},
		Resource: resource,
		Points:   []*monitoring_pb.Point{point},
	}

	switch metadata.Type {
	case textparse.MetricTypeCounter:
		res.MetricKind = metric_pb.MetricDescriptor_CUMULATIVE
		res.ValueType = metric_pb.MetricDescriptor_DOUBLE

		point.Interval.StartTime = getTimestamp(1) // TODO(fabxc): replace with proper reset timestamp.
		point.Value = &monitoring_pb.TypedValue{&monitoring_pb.TypedValue_DoubleValue{sample.V}}

	case textparse.MetricTypeGauge, textparse.MetricTypeUntyped:
		res.MetricKind = metric_pb.MetricDescriptor_GAUGE
		res.ValueType = metric_pb.MetricDescriptor_DOUBLE

		point.Value = &monitoring_pb.TypedValue{&monitoring_pb.TypedValue_DoubleValue{sample.V}}

	case textparse.MetricTypeSummary:
		switch suffix {
		case metricSuffixSum:
			res.MetricKind = metric_pb.MetricDescriptor_CUMULATIVE
			res.ValueType = metric_pb.MetricDescriptor_DOUBLE
			point.Interval.StartTime = getTimestamp(1) // TODO(fabxc): replace with proper reset timestamp.
			point.Value = &monitoring_pb.TypedValue{&monitoring_pb.TypedValue_DoubleValue{sample.V}}
		case metricSuffixCount:
			res.MetricKind = metric_pb.MetricDescriptor_CUMULATIVE
			res.ValueType = metric_pb.MetricDescriptor_INT64
			point.Interval.StartTime = getTimestamp(1) // TODO(fabxc): replace with proper reset timestamp.
			point.Value = &monitoring_pb.TypedValue{&monitoring_pb.TypedValue_Int64Value{int64(sample.V)}}
		case "": // Actual quantiles.
			res.MetricKind = metric_pb.MetricDescriptor_GAUGE
			res.ValueType = metric_pb.MetricDescriptor_DOUBLE
			point.Value = &monitoring_pb.TypedValue{&monitoring_pb.TypedValue_DoubleValue{sample.V}}
		default:
			return res, samples[1:], errors.Errorf("unexpected metric name suffix %q", suffix)
		}

	case textparse.MetricTypeHistogram:
		// The metric is set to the base name and the le label must be stripped.
		// buildDistribution uses the cleaned up label set and base name to detect series
		// belonging to the same histogram.
		res.Metric.Type = getMetricType(baseMetricName)

		res.MetricKind = metric_pb.MetricDescriptor_CUMULATIVE
		res.ValueType = metric_pb.MetricDescriptor_DISTRIBUTION

		// We pass in the original lset for matching since Prometheus's target label must
		// be the same as well.
		var v *distribution_pb.Distribution
		v, samples = b.buildDistribution(baseMetricName, lset, samples)

		point.Interval.StartTime = getTimestamp(1) // TODO(fabxc): replace with proper reset timestamp.
		point.Value = &monitoring_pb.TypedValue{
			Value: &monitoring_pb.TypedValue_DistributionValue{v},
		}
		return res, samples, nil

	default:
		return nil, samples[1:], errors.Errorf("unexpected metric type %s", metadata.Type)
	}
	return res, samples[1:], nil
}

const (
	metricSuffixBucket = "_bucket"
	metricSuffixSum    = "_sum"
	metricSuffixCount  = "_count"
)

func stripComplexMetricSuffix(name string) (string, string, bool) {
	if strings.HasSuffix(name, metricSuffixBucket) {
		return name[:len(name)-len(metricSuffixBucket)], metricSuffixBucket, true
	}
	if strings.HasSuffix(name, metricSuffixCount) {
		return name[:len(name)-len(metricSuffixCount)], metricSuffixCount, true
	}
	if strings.HasSuffix(name, metricSuffixSum) {
		return name[:len(name)-len(metricSuffixSum)], metricSuffixSum, true
	}
	return name, "", false
}

const (
	maxLabelCount = 10
	metricsPrefix = "external.googleapis.com/prometheus"
)

func getMetricType(promName string) string {
	return metricsPrefix + "/" + promName
}

func getMetricKind(t textparse.MetricType) metric_pb.MetricDescriptor_MetricKind {
	if t == textparse.MetricTypeCounter || t == textparse.MetricTypeHistogram {
		return metric_pb.MetricDescriptor_CUMULATIVE
	}
	return metric_pb.MetricDescriptor_GAUGE
}

func getValueType(t textparse.MetricType) metric_pb.MetricDescriptor_ValueType {
	if t == textparse.MetricTypeHistogram {
		return metric_pb.MetricDescriptor_DISTRIBUTION
	}
	return metric_pb.MetricDescriptor_DOUBLE
}

// getTimestamp converts a millisecond timestamp into a protobuf timestamp.
func getTimestamp(t int64) *timestamp_pb.Timestamp {
	return &timestamp_pb.Timestamp{
		Seconds: t / 1000,
		Nanos:   int32((t % 1000) * int64(time.Millisecond)),
	}
}

func (b *sampleBuilder) getResource(lset labels.Labels) (*monitoredres_pb.MonitoredResource, bool) {
	for _, m := range b.resourceMaps {
		if lset := m.Translate(lset); lset != nil {
			return &monitoredres_pb.MonitoredResource{
				Type:   m.Type,
				Labels: lset,
			}, true
		}
	}
	return nil, false
}

type distribution struct {
	bounds []float64
	values []int64
}

func (d *distribution) Len() int {
	return len(d.bounds)
}

func (d *distribution) Less(i, j int) bool {
	return d.bounds[i] < d.bounds[j]
}

func (d *distribution) Swap(i, j int) {
	d.bounds[i], d.bounds[j] = d.bounds[j], d.bounds[i]
	d.values[i], d.values[j] = d.values[j], d.values[i]
}

// buildDistribution consumes series from the beginning of the input slice that belong to a histogram
// with the given metric name and label set.
func (b *sampleBuilder) buildDistribution(baseName string, matchLset tsdbLabels.Labels, samples []tsdb.RefSample) (*distribution_pb.Distribution, []tsdb.RefSample) {
	var (
		consumed   int
		count, sum float64
		dist       = distribution{bounds: make([]float64, 0, 20), values: make([]int64, 0, 20)}
	)
	// We assume that all series belonging to the histogram are sequential. Consume series
	// until we hit a new metric.
	// We do not assume that the series list buckets in ascending order. This is more likely to
	// not be the case depending on the implementation of client.
Loop:
	for _, s := range samples {
		lset, ok := b.series.get(s.Ref)
		if !ok {
			consumed++
			// TODO(fabxc): increment metric.
			continue
		}
		name := lset.Get("__name__")
		// The series matches if it has the same base name, the remainder is a valid histogram suffix,
		// and the labels aside from the le and __name__ label match up.
		if !strings.HasPrefix(name, baseName) || !histogramLabelsEqual(lset, matchLset) {
			break
		}
		switch name[len(baseName):] {
		case metricSuffixSum:
			sum = s.V
		case metricSuffixCount:
			count = s.V
		case metricSuffixBucket:
			upper, err := strconv.ParseFloat(lset.Get("le"), 64)
			if err != nil {
				consumed++
				// TODO(fabxc): increment metric.
				continue
			}
			dist.bounds = append(dist.bounds, upper)
			dist.values = append(dist.values, int64(s.V))
		default:
			break Loop
		}
		consumed++
	}

	// Ensure buckets are increasing.
	sort.Sort(&dist)
	// Reuse slices we already populated to build final bounds and values.
	var (
		bounds           = dist.bounds[:0]
		values           = dist.values[:0]
		mean, dev, lower float64
		prevVal          int64
	)
	if count > 0 {
		mean = sum / count
	}
	for i, upper := range dist.bounds {
		if math.IsInf(upper, 1) {
			upper = lower
		} else {
			bounds = append(bounds, upper)
		}

		val := dist.values[i] - prevVal
		x := (lower + upper) / 2
		dev += float64(val) * (x - mean) * (x - mean)

		lower = upper
		prevVal = dist.values[i]
		values = append(values, val)
	}
	d := &distribution_pb.Distribution{
		Count: int64(count),
		Mean:  mean,
		SumOfSquaredDeviation: dev,
		BucketOptions: &distribution_pb.Distribution_BucketOptions{
			Options: &distribution_pb.Distribution_BucketOptions_ExplicitBuckets{
				ExplicitBuckets: &distribution_pb.Distribution_BucketOptions_Explicit{
					Bounds: bounds,
				},
			},
		},
		BucketCounts: values,
	}
	return d, samples[consumed:]
}

// histogramLabelsEqual checks whether two label sets for a histogram series are equal aside from their
// le and __name__ labels.
func histogramLabelsEqual(a, b tsdbLabels.Labels) bool {
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		if a[i].Name == "le" || a[i].Name == "__name__" {
			i++
			continue
		}
		if b[j].Name == "le" || b[j].Name == "__name__" {
			j++
			continue
		}
		if a[i] != b[j] {
			return false
		}
		i++
		j++
	}
	// Consume trailing le and __name__ labels so the check below passes correctly.
	for i < len(a) {
		if a[i].Name == "le" || a[i].Name == "__name__" {
			i++
			continue
		}
		break
	}
	for j < len(b) {
		if b[j].Name == "le" || b[j].Name == "__name__" {
			j++
			continue
		}
		break
	}
	// If one label set still has labels left, they are not equal.
	return i == len(a) && j == len(b)
}
