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
	"time"

	"github.com/Stackdriver/stackdriver-prometheus-sidecar/targets"
	timestamp_pb "github.com/golang/protobuf/ptypes/timestamp"
	"github.com/pkg/errors"
	"github.com/prometheus/prometheus/pkg/labels"
	"github.com/prometheus/prometheus/pkg/textparse"
	"github.com/prometheus/tsdb"
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
		return nil, samples[1:], errors.Errorf("couldn't infer resource for target %s", target.DiscoveredLabels)
	}
	// TODO(fabxc): this needs special handling for summaries and histograms since we need to strip
	// suffices like _bucket, _sum, and _count.
	// However, they all may be used for counters and gauges as well, so we cannot always strip them.
	// Probably best to just probe for both. Right now we don't do anything since we don't care about
	// summaries und histograms yet.
	metadata, err := b.metadata.Get(ctx, lset.Get("job"), lset.Get("instance"), lset.Get("__name__"))
	if err != nil {
		return nil, samples, errors.Wrap(err, "get metadata")
	}
	if metadata == nil {
		// TODO(fabxc): increment a metric.
		return nil, samples[1:], nil
	}

	// TODO(fabxc): complex metric types skipped for now.
	if metadata.Type == textparse.MetricTypeHistogram || metadata.Type == textparse.MetricTypeSummary {
		return nil, samples[1:], nil
	}
	interval := &monitoring_pb.TimeInterval{
		EndTime: getTimestamp(sample.T),
	}
	if getMetricKind(metadata.Type) == metric_pb.MetricDescriptor_CUMULATIVE {
		// TODO(fabxc): infer the correct reset timestamp.
		interval.StartTime = getTimestamp(1)
	}
	point := &monitoring_pb.Point{
		Interval: interval,
		Value:    &monitoring_pb.TypedValue{&monitoring_pb.TypedValue_DoubleValue{DoubleValue: sample.V}},
	}
	res := &monitoring_pb.TimeSeries{
		Metric: &metric_pb.Metric{
			Type:   getMetricType(lset.Get("__name__")),
			Labels: finalLabels.Map(),
		},
		Resource:   resource,
		MetricKind: getMetricKind(metadata.Type),
		ValueType:  getValueType(metadata.Type),
		Points:     []*monitoring_pb.Point{point},
	}
	return res, samples[1:], nil
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
