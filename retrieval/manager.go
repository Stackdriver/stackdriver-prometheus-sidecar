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
	"encoding/json"
	"io/ioutil"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/Stackdriver/stackdriver-prometheus-sidecar/metadata"
	"github.com/Stackdriver/stackdriver-prometheus-sidecar/tail"
	"github.com/Stackdriver/stackdriver-prometheus-sidecar/targets"
	"github.com/go-kit/kit/log"
	"github.com/go-kit/kit/log/level"
	"github.com/prometheus/prometheus/pkg/labels"
	"github.com/prometheus/tsdb"
	"github.com/prometheus/tsdb/fileutil"
	tsdblabels "github.com/prometheus/tsdb/labels"
	"github.com/prometheus/tsdb/wal"
	"go.opencensus.io/stats"
	"go.opencensus.io/stats/view"
	monitoring_pb "google.golang.org/genproto/googleapis/monitoring/v3"
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
	Get(ctx context.Context, job, instance, metric string) (*metadata.Entry, error)
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
	filtersets [][]*labels.Matcher,
	metricRenames map[string]string,
	targetGetter TargetGetter,
	metadataGetter MetadataGetter,
	appender Appender,
	metricsPrefix string,
	useGkeResource bool,
	counterAggregator *CounterAggregator,
) *PrometheusReader {
	if logger == nil {
		logger = log.NewNopLogger()
	}
	return &PrometheusReader{
		appender:             appender,
		logger:               logger,
		tailer:               tailer,
		filtersets:           filtersets,
		walDirectory:         walDirectory,
		targetGetter:         targetGetter,
		metadataGetter:       metadataGetter,
		progressSaveInterval: time.Minute,
		metricRenames:        metricRenames,
		metricsPrefix:        metricsPrefix,
		useGkeResource:       useGkeResource,
		counterAggregator:    counterAggregator,
	}
}

type PrometheusReader struct {
	logger               log.Logger
	walDirectory         string
	tailer               *tail.Tailer
	filtersets           [][]*labels.Matcher
	metricRenames        map[string]string
	targetGetter         TargetGetter
	metadataGetter       MetadataGetter
	appender             Appender
	progressSaveInterval time.Duration
	metricsPrefix        string
	useGkeResource       bool
	counterAggregator    *CounterAggregator
}

var (
	samplesProcessed = stats.Int64("prometheus_sidecar/samples_processed", "Number of WAL samples processed", stats.UnitDimensionless)
	samplesProduced  = stats.Int64("prometheus_sidecar/samples_produced", "Number of Stackdriver samples produced", stats.UnitDimensionless)
)

func init() {
	view.Register(&view.View{
		Name:        "prometheus_sidecar/batches_processed",
		Description: "Total number of sample batches processed",
		Measure:     samplesProcessed,
		Aggregation: view.Count(),
	})
	view.Register(&view.View{
		Name:        "prometheus_sidecar/samples_processed",
		Description: "Number of WAL samples processed",
		Measure:     samplesProcessed,
		Aggregation: view.Sum(),
	})
	view.Register(&view.View{
		Name:        "prometheus_sidecar/samples_produced",
		Description: "Number of Stackdriver samples produced",
		Measure:     samplesProduced,
		Aggregation: view.Sum(),
	})
}

func (r *PrometheusReader) Run(ctx context.Context, startOffset int) error {
	level.Info(r.logger).Log("msg", "Starting Prometheus reader...")

	seriesCache := newSeriesCache(
		r.logger,
		r.walDirectory,
		r.filtersets,
		r.metricRenames,
		r.targetGetter,
		r.metadataGetter,
		ResourceMappings,
		r.metricsPrefix,
		r.useGkeResource,
		r.counterAggregator,
	)
	go seriesCache.run(ctx)

	builder := &sampleBuilder{series: seriesCache}

	// NOTE(fabxc): wrap the tailer into a buffered reader once we become concerned
	// with performance. The WAL reader will do a lot of tiny reads otherwise.
	// This is also the reason for the series cache dealing with "maxSegment" hints
	// for series rather than precise ones.
	var (
		started  = false
		skipped  = 0
		reader   = wal.NewReader(r.tailer)
		err      error
		lastSave time.Time
		samples  []tsdb.RefSample
		series   []tsdb.RefSeries
	)
Outer:
	for reader.Next() {
		offset := r.tailer.Offset()
		record := reader.Record()

		if offset > startOffset && time.Since(lastSave) > r.progressSaveInterval {
			if err := SaveProgressFile(r.walDirectory, offset); err != nil {
				level.Error(r.logger).Log("msg", "saving progress failed", "err", err)
			} else {
				lastSave = time.Now()
			}
		}
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
			// Skip sample records before the the boundary offset.
			if offset < startOffset {
				skipped++
				continue
			}
			if !started {
				level.Info(r.logger).Log("msg", "reached first record after start offset",
					"start_offset", startOffset, "skipped_records", skipped)
				started = true
			}
			samples, err = decoder.Samples(record, samples[:0])
			if err != nil {
				level.Error(r.logger).Log("error", err)
				continue
			}
			backoff := time.Duration(0)
			// Do not increment the metric for produced samples each time but rather
			// once at the end.
			// Otherwise it will increase CPU usage by ~10%.
			processed, produced := len(samples), 0

			for len(samples) > 0 {
				select {
				case <-ctx.Done():
					break Outer
				default:
				}
				// We intentionally don't use time.After in the select statement above
				// since we'd unnecessarily spawn a new goroutine for each sample
				// we process even when there are no errors.
				if backoff > 0 {
					time.Sleep(backoff)
				}

				var outputSample *monitoring_pb.TimeSeries
				var hash uint64
				outputSample, hash, samples, err = builder.next(ctx, samples)
				if err != nil {
					level.Warn(r.logger).Log("msg", "Failed to build sample", "err", err)
					backoff = exponential(backoff)
					continue
				}
				if outputSample == nil {
					continue
				}
				r.appender.Append(hash, outputSample)
				produced++
			}
			stats.Record(ctx, samplesProcessed.M(int64(processed)), samplesProduced.M(int64(produced)))

		case tsdb.RecordTombstones:
		}
	}
	level.Info(r.logger).Log("msg", "Done processing WAL.")
	return reader.Err()
}

const (
	progressFilename     = "stackdriver_sidecar.json"
	progressBufferMargin = 512 * 1024
)

// progress defines the JSON object of the progress file.
type progress struct {
	// Approximate WAL offset of last synchronized records in bytes.
	Offset int `json:"offset"`
}

// ReadPRogressFile reads the progress file in the given directory and returns
// the saved offset.
func ReadProgressFile(dir string) (offset int, err error) {
	b, err := ioutil.ReadFile(filepath.Join(dir, progressFilename))
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	var p progress
	if err := json.Unmarshal(b, &p); err != nil {
		return 0, err
	}
	return p.Offset, nil
}

// SaveProgressFile saves a progress file with the given offset in directory.
func SaveProgressFile(dir string, offset int) error {
	// Adjust offset to account for buffered records that possibly haven't been
	// written yet.
	b, err := json.Marshal(progress{Offset: offset - progressBufferMargin})
	if err != nil {
		return err
	}
	tmp := filepath.Join(dir, progressFilename+".tmp")
	if err := ioutil.WriteFile(tmp, b, 0666); err != nil {
		return err
	}
	if err := fileutil.Rename(tmp, filepath.Join(dir, progressFilename)); err != nil {
		return err
	}
	return nil
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

func exponential(d time.Duration) time.Duration {
	const (
		min = 10 * time.Millisecond
		max = 2 * time.Second
	)
	d *= 2
	if d < min {
		d = min
	}
	if d > max {
		d = max
	}
	return d
}
