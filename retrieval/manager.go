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
	"sort"

	"github.com/Stackdriver/stackdriver-prometheus-sidecar/tail"
	"github.com/Stackdriver/stackdriver-prometheus-sidecar/targets"
	"github.com/go-kit/kit/log"
	"github.com/go-kit/kit/log/level"
	monitoring_pb "google.golang.org/genproto/googleapis/monitoring/v3"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/prometheus/pkg/labels"
	"github.com/prometheus/prometheus/scrape"
	"github.com/prometheus/tsdb"
	tsdblabels "github.com/prometheus/tsdb/labels"
	"github.com/prometheus/tsdb/wal"
)

type TargetGetter interface {
	Get(ctx context.Context, lset labels.Labels) (*targets.Target, error)
}

type targetsWithDiscoveredLabels struct {
	TargetGetter
	lset labels.Labels
}

// TargetsWithDiscoveredLabels wraps a TargetGetter and adds a static set of labels to the discovered
// labels of all targets retrieved from it.
func TargetsWithDiscoveredLabels(tg TargetGetter, lset labels.Labels) TargetGetter {
	return &targetsWithDiscoveredLabels{TargetGetter: tg, lset: lset}
}

func (tg *targetsWithDiscoveredLabels) Get(ctx context.Context, lset labels.Labels) (*targets.Target, error) {
	t, err := tg.TargetGetter.Get(ctx, lset)
	if err != nil || t == nil {
		return t, err
	}
	repl := *t
	repl.DiscoveredLabels = append(append(labels.Labels{}, t.DiscoveredLabels...), tg.lset...)
	sort.Sort(repl.DiscoveredLabels)
	return &repl, nil
}

type MetadataGetter interface {
	Get(ctx context.Context, job, instance, metric string) (*scrape.MetricMetadata, error)
}

// Appender appends a time series with exactly one data point. A hash for the series
// (but not the data point) must be provided.
// The client may cache the computed hash more easily, which is why its part of the call
// and not done by the Appender's implementation.
type Appender interface {
	Append(hash uint64, s *monitoring_pb.TimeSeries) error
}

// NewPrometheusReader is the PrometheusReader constructor
func NewPrometheusReader(
	logger log.Logger,
	walDirectory string,
	tailer *tail.Tailer,
	targetGetter TargetGetter,
	metadataGetter MetadataGetter,
	appender Appender,
) *PrometheusReader {
	return &PrometheusReader{
		appender:       appender,
		logger:         logger,
		tailer:         tailer,
		walDirectory:   walDirectory,
		targetGetter:   targetGetter,
		metadataGetter: metadataGetter,
	}
}

type PrometheusReader struct {
	logger         log.Logger
	walDirectory   string
	tailer         *tail.Tailer
	targetGetter   TargetGetter
	metadataGetter MetadataGetter
	appender       Appender
}

var samplesProcessed = prometheus.NewCounter(prometheus.CounterOpts{
	Name: "prometheus_sidecar_wal_samples_processed_total",
	Help: "Total number of samples processed from the WAL",
})

func init() {
	prometheus.MustRegister(samplesProcessed)
}

func (r *PrometheusReader) Run(ctx context.Context) error {
	level.Info(r.logger).Log("msg", "Starting Prometheus reader...")

	seriesCache := newSeriesCache(r.logger, r.walDirectory, r.targetGetter, r.metadataGetter, ResourceMappings)
	go seriesCache.run(ctx)

	builder := &sampleBuilder{series: seriesCache}

	// NOTE(fabxc): wrap the tailer into a buffered reader once we become concerned
	// with performance. The WAL reader will do a lot of tiny reads otherwise.
	// This is also the reason for the series cache dealing with "maxSegment" hints
	// for series rather than precise ones.
	var (
		err     error
		reader  = wal.NewReader(r.tailer)
		samples []tsdb.RefSample
		series  []tsdb.RefSeries
	)
Outer:
	for reader.Next() {
		record := reader.Record()

		var decoder tsdb.RecordDecoder
		switch decoder.Type(record) {
		case tsdb.RecordSeries:
			series, err = decoder.Series(record, series[:0])
			if err != nil {
				level.Error(r.logger).Log("error", err)
				continue
			}
			for _, s := range series {
				seriesCache.set(ctx, s.Ref, s.Labels, r.tailer.CurrentSegment())
			}
		case tsdb.RecordSamples:
			samples, err = decoder.Samples(record, samples[:0])
			if err != nil {
				level.Error(r.logger).Log("error", err)
				continue
			}
			for len(samples) > 0 {
				select {
				case <-ctx.Done():
					break Outer
				default:
				}
				var outputSample *monitoring_pb.TimeSeries
				var hash uint64
				outputSample, hash, samples, err = builder.next(ctx, samples)
				if err != nil {
					level.Warn(r.logger).Log("msg", "Failed to build sample", "err", err)
					continue
				}
				if outputSample == nil {
					continue
				}
				r.appender.Append(hash, outputSample)
				samplesProcessed.Inc()
			}
		case tsdb.RecordTombstones:
		}
	}
	level.Info(r.logger).Log("msg", "Done processing WAL.")
	return reader.Err()
}

// TODO(jkohen): We should be able to avoid this conversion.
func pkgLabels(input tsdblabels.Labels) labels.Labels {
	output := make(labels.Labels, 0, len(input))
	for _, l := range input {
		output = append(output, labels.Label(l))
	}
	return output
}

func hashSeries(s *monitoring_pb.TimeSeries) uint64 {
	const sep = '\xff'
	h := hashNew()

	h = hashAdd(h, s.Resource.Type)
	h = hashAddByte(h, sep)
	h = hashAdd(h, s.Metric.Type)

	// Map iteration is randomized. We thus convert the labels to sorted slices
	// with labels.FromMap before hashing.
	for _, l := range labels.FromMap(s.Resource.Labels) {
		h = hashAddByte(h, sep)
		h = hashAdd(h, l.Name)
		h = hashAddByte(h, sep)
		h = hashAdd(h, l.Value)
	}
	h = hashAddByte(h, sep)
	for _, l := range labels.FromMap(s.Metric.Labels) {
		h = hashAddByte(h, sep)
		h = hashAdd(h, l.Name)
		h = hashAddByte(h, sep)
		h = hashAdd(h, l.Value)
	}
	return h
}
