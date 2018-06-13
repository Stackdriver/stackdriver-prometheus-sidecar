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
	"fmt"
	"net/url"

	"github.com/Stackdriver/stackdriver-prometheus-sidecar/tail"
	"github.com/Stackdriver/stackdriver-prometheus-sidecar/targets"
	"github.com/go-kit/kit/log"
	"github.com/go-kit/kit/log/level"
	"github.com/gogo/protobuf/proto"
	"github.com/pkg/errors"

	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/prometheus/pkg/labels"
	"github.com/prometheus/tsdb"
	tsdblabels "github.com/prometheus/tsdb/labels"
	"github.com/prometheus/tsdb/wal"
)

// NewPrometheusReader is the PrometheusReader constructor
func NewPrometheusReader(logger log.Logger, walDirectory string, promURL *url.URL, appender Appender) *PrometheusReader {
	return &PrometheusReader{
		appender:     appender,
		logger:       logger,
		walDirectory: walDirectory,
		promURL:      promURL,
	}
}

type PrometheusReader struct {
	logger       log.Logger
	walDirectory string
	promURL      *url.URL
	appender     Appender
	cancelTail   context.CancelFunc
}

func (r *PrometheusReader) Run() error {
	level.Info(r.logger).Log("msg", "Starting Prometheus reader...")
	var ctx context.Context
	ctx, r.cancelTail = context.WithCancel(context.Background())
	tailer, err := tail.Tail(ctx, r.walDirectory)
	if err != nil {
		level.Error(r.logger).Log("error", err)
		return err
	}
	seriesCache := newSeriesCache(r.logger, r.walDirectory)
	go seriesCache.run(ctx)

	targetCache := targets.NewCache(r.logger, nil, r.promURL)
	go targetCache.Run(ctx)

	// NOTE(fabxc): wrap the tailer into a buffered reader once we become concerned
	// with performance. The WAL reader will do a lot of tiny reads otherwise.
	// This is also the reason for the series cache dealing with "maxSegment" hints
	// for series rather than precise ones.
	reader := wal.NewReader(tailer)
	for reader.Next() {
		if reader.Err() != nil {
			return reader.Err()
		}
		record := reader.Record()
		var decoder tsdb.RecordDecoder
		switch decoder.Type(record) {
		case tsdb.RecordSeries:
			recordSeries, err := decoder.Series(record, nil)
			if err != nil {
				level.Error(r.logger).Log("error", err)
				continue
			}
			for _, series := range recordSeries {
				seriesCache.set(series.Ref, series.Labels, tailer.CurrentSegment())
			}
		case tsdb.RecordSamples:
			recordSamples, err := decoder.Samples(record, nil)
			if err != nil {
				level.Error(r.logger).Log("error", err)
				continue
			}
			for len(recordSamples) > 0 {
				var outputSample *MetricFamily
				outputSample, recordSamples, err = buildSample(ctx, seriesCache, targetCache, recordSamples)
				if err != nil {
					level.Warn(r.logger).Log("msg", "Failed to build sample", "err", err)
					continue
				}
				r.appender.Append(outputSample)
			}
		case tsdb.RecordTombstones:
		}
	}
	level.Info(r.logger).Log("msg", "Done processing WAL.")
	return nil
}

// Stop cancels the reader and blocks until it has exited.
func (r *PrometheusReader) Stop() {
	r.cancelTail()
}

type Getter interface {
	Get(ctx context.Context, lset labels.Labels) (*targets.Target, error)
}

// Creates a MetricFamily instance from the head of recordSamples, or error if
// that fails. In either case, this function returns the recordSamples items
// that weren't consumed.
func buildSample(ctx context.Context, seriesGetter seriesGetter, targetGetter Getter, recordSamples []tsdb.RefSample) (*MetricFamily, []tsdb.RefSample, error) {
	sample := recordSamples[0]
	tsdblset, ok := seriesGetter.get(sample.Ref)
	if !ok {
		return nil, recordSamples[1:], fmt.Errorf("No series matched sample by ref %v", sample)
	}
	lset := pkgLabels(tsdblset)
	// Fill in the discovered labels from the Targets API.
	target, err := targetGetter.Get(ctx, lset)
	if err != nil {
		return nil, recordSamples[1:], errors.Wrapf(err, "No target matched labels %v", lset)
	}
	metricLabels := targets.DropTargetLabels(lset, target.Labels)
	// TODO(jkohen): Rebuild histograms and summary from individual time series.
	metricFamily := &dto.MetricFamily{
		Metric: []*dto.Metric{{}},
	}
	metric := metricFamily.Metric[0]
	metric.Label = make([]*dto.LabelPair, 0, len(metricLabels)-1)
	for _, l := range metricLabels {
		if l.Name == labels.MetricName {
			metricFamily.Name = proto.String(l.Value)
			continue
		}
		metric.Label = append(metric.Label, &dto.LabelPair{
			Name:  proto.String(l.Name),
			Value: proto.String(l.Value),
		})
	}
	// TODO(jkohen): Support all metric types and populate Help metadata.
	metricFamily.Type = dto.MetricType_UNTYPED.Enum()
	metric.Untyped = &dto.Untyped{Value: proto.Float64(sample.V)}
	metric.TimestampMs = proto.Int64(sample.T)
	// TODO(jkohen): track reset timestamps.
	metricResetTimestampMs := []int64{NoTimestamp}
	m, err := NewMetricFamily(metricFamily, metricResetTimestampMs, target.DiscoveredLabels)
	return m, recordSamples[1:], err
}

// TODO(jkohen): We should be able to avoid this conversion.
func pkgLabels(input tsdblabels.Labels) labels.Labels {
	output := make(labels.Labels, 0, len(input))
	for _, l := range input {
		output = append(output, labels.Label(l))
	}
	return output
}
