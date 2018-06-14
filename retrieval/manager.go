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
	t.DiscoveredLabels = append(append(labels.Labels{}, t.DiscoveredLabels...), tg.lset...)
	sort.Sort(t.DiscoveredLabels)
	return t, nil
}

type MetadataGetter interface {
	Get(ctx context.Context, job, instance, metric string) (*scrape.MetricMetadata, error)
}

type Appender interface {
	Append(hash uint64, s *monitoring_pb.TimeSeries) error
}

// NewPrometheusReader is the PrometheusReader constructor
func NewPrometheusReader(
	logger log.Logger,
	walDirectory string,
	targetGetter TargetGetter,
	metadataGetter MetadataGetter,
	appender Appender,
) *PrometheusReader {
	return &PrometheusReader{
		appender:       appender,
		logger:         logger,
		walDirectory:   walDirectory,
		targetGetter:   targetGetter,
		metadataGetter: metadataGetter,
	}
}

type PrometheusReader struct {
	logger         log.Logger
	walDirectory   string
	targetGetter   TargetGetter
	metadataGetter MetadataGetter
	appender       Appender
	cancelTail     context.CancelFunc
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
				// Hashing labels is expensive – just use the reference as one.
				// NOTE(fabxc): may this become an issue if multi-series samples are exposed with
				// inconsistent orders across scrapes? We could use the lowest one of all involved series then.
				hash := recordSamples[0].Ref
				var outputSample *monitoring_pb.TimeSeries
				outputSample, recordSamples, err = buildSample(ctx, seriesCache, r.targetGetter, r.metadataGetter, recordSamples)
				if err != nil {
					level.Warn(r.logger).Log("msg", "Failed to build sample", "err", err)
					continue
				}
				r.appender.Append(hash, outputSample)
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

// TODO(jkohen): We should be able to avoid this conversion.
func pkgLabels(input tsdblabels.Labels) labels.Labels {
	output := make(labels.Labels, 0, len(input))
	for _, l := range input {
		output = append(output, labels.Label(l))
	}
	return output
}
