// Copyright 2013 The Prometheus Authors
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package retrieval

import (
	"sync"

	"github.com/go-kit/kit/log"
	"github.com/go-kit/kit/log/level"
	"github.com/gogo/protobuf/proto"

	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/model"
	"github.com/prometheus/prometheus/config"
	"github.com/prometheus/prometheus/pkg/labels"
	"github.com/prometheus/tsdb"
	"github.com/prometheus/tsdb/wal"
)

// Appendable returns an Appender.
type Appendable interface {
	Appender() (Appender, error)
}

// NewPrometheusReader is the PrometheusReader constructor
func NewPrometheusReader(logger log.Logger, walDirectory string, app Appendable) *PrometheusReader {
	// TODO(jkohen): change the input to an Appender
	appender, err := app.Appender()
	if err != nil {
		panic(err)
	}
	return &PrometheusReader{
		appender:     appender,
		logger:       logger,
		walDirectory: walDirectory,
		graceShut:    make(chan struct{}),
	}
}

type PrometheusReader struct {
	logger       log.Logger
	walDirectory string
	appender     Appender
	mtx          sync.RWMutex
	graceShut    chan struct{}
}

func (r *PrometheusReader) Run() error {
	level.Info(r.logger).Log("msg", "Starting Prometheus reader...")
	segmentsReader, err := wal.NewSegmentsReader(r.walDirectory)
	if err != nil {
		level.Error(r.logger).Log("error", err)
		return err
	}
	refs := map[uint64]tsdb.RefSeries{}
	reader := wal.NewReader(segmentsReader)
	var sampleCount int
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
				refs[series.Ref] = series
			}
		case tsdb.RecordSamples:
			recordSamples, err := decoder.Samples(record, nil)
			if err != nil {
				level.Error(r.logger).Log("error", err)
				continue
			}
			for _, sample := range recordSamples {
				series, ok := refs[sample.Ref]
				if !ok {
					level.Warn(r.logger).Log("msg", "Unknown series ref in sample", "sample", sample)
					continue
				}
				sampleCount++
				if sampleCount < 10 {
					level.Info(r.logger).Log("msg", "Sample found", "sampleRef", sample.Ref, "sampleT", sample.T, "sampleV", sample.V, "series", series.Labels)
				}
				// TODO(jkohen): Rebuild histograms and summary from individual time series.
				metricFamily := &dto.MetricFamily{
					Metric: []*dto.Metric{{}},
				}
				metric := metricFamily.Metric[0]
				metric.Label = make([]*dto.LabelPair, len(series.Labels))
				for i := range series.Labels {
					if series.Labels[i].Name == model.MetricNameLabel {
						metricFamily.Name = proto.String(series.Labels[i].Value)
					}
					metric.Label[i] = &dto.LabelPair{
						Name:  proto.String(series.Labels[i].Name),
						Value: proto.String(series.Labels[i].Value),
					}
				}
				// TODO(jkohen): Support all metric types and populate Help metadata.
				metricFamily.Type = dto.MetricType_UNTYPED.Enum()
				metric.Untyped = &dto.Untyped{Value: proto.Float64(sample.V)}
				metric.TimestampMs = proto.Int64(sample.T)
				// TODO(jkohen): track reset timestamps.
				metricResetTimestampMs := []int64{NoTimestamp}
				// TODO(jkohen): fill in the discovered labels from the Targets API.
				targetLabels := make(labels.Labels, len(series.Labels))
				for i := range series.Labels {
					targetLabels[i] =
						labels.Label{series.Labels[i].Name, series.Labels[i].Value}
				}
				f, err := NewMetricFamily(metricFamily, metricResetTimestampMs, targetLabels)
				if err != nil {
					level.Warn(r.logger).Log("msg", "Cannot construct MetricFamily", "err", err)
					continue
				}
				r.appender.Add(f)
			}
		case tsdb.RecordTombstones:
		}
	}
	level.Info(r.logger).Log("msg", "Done processing WAL.")
	for {
		select {
		case <-r.graceShut:
			return nil
		}
	}
}

// Stop cancels the reader and blocks until it has exited.
func (r *PrometheusReader) Stop() {
	close(r.graceShut)
}

// ApplyConfig resets the manager's target providers and job configurations as defined by the new cfg.
func (r *PrometheusReader) ApplyConfig(cfg *config.Config) error {
	r.mtx.Lock()
	defer r.mtx.Unlock()
	return nil
}
