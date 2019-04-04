// Copyright 2019 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package retrieval

import (
	"context"
	"math"

	"github.com/go-kit/kit/log"
	"github.com/go-kit/kit/log/level"
	promlabels "github.com/prometheus/prometheus/pkg/labels"
	"github.com/prometheus/tsdb/labels"
	"go.opencensus.io/stats"
	"go.opencensus.io/stats/view"
)

var statsRecord = stats.Record

const metricPrefix = "prometheus_sidecar/aggregated_counters/"

// CounterAggregator provides the 'aggregated counters' feature of the sidecar.
// It can be used to export a sum of multiple counters from Prometheus to
// Stackdriver as a single cumulative metric.
type CounterAggregator struct {
	logger   log.Logger
	counters []*aggregatedCounter
}

// aggregatedCounter is where CounterAggregator keeps internal state about each
// exported metric: OpenCensus measure and view as well as a list of Matchers that
// define which Prometheus metrics will get aggregated.
type aggregatedCounter struct {
	measure  *stats.Float64Measure
	view     *view.View
	matchers [][]*promlabels.Matcher
}

// CounterAggregatorConfig contains configuration for CounterAggregator. Keys of the map
// are metric names that will be exported by counter aggregator.
type CounterAggregatorConfig map[string]*CounterAggregatorMetricConfig

// CounterAggregatorMetricConfig provides configuration of a single aggregated counter.
// Matchers specify what Prometheus metrics (which are expected to be counter metrics) will
// be re-aggregated. Help provides a description for the exported metric.
type CounterAggregatorMetricConfig struct {
	Matchers [][]*promlabels.Matcher
	Help     string
}

// counterTracker keeps track of a single time series that has at least one aggregated
// counter associated with it (i.e. there is at least one aggregated counter that has
// Matchers covering this time series). Last timestamp and value are tracked
// to detect counter resets.
type counterTracker struct {
	lastTimestamp int64
	lastValue     float64
	measures      []*stats.Float64Measure
	logger        log.Logger
}

// NewCounterAggregator creates a counter aggregator.
func NewCounterAggregator(logger log.Logger, config *CounterAggregatorConfig) (*CounterAggregator, error) {
	aggregator := &CounterAggregator{logger: logger}
	for metric, cfg := range *config {
		name := metricPrefix + metric
		measure := stats.Float64(name, cfg.Help, stats.UnitDimensionless)
		v := &view.View{
			Name:        name,
			Description: cfg.Help,
			Measure:     measure,
			Aggregation: view.Sum(),
		}
		if err := view.Register(v); err != nil {
			return nil, err
		}
		aggregator.counters = append(aggregator.counters, &aggregatedCounter{
			measure:  measure,
			view:     v,
			matchers: cfg.Matchers,
		})
	}
	return aggregator, nil
}

// Close must be called when CounterAggregator is no longer needed.
func (c *CounterAggregator) Close() {
	for _, counter := range c.counters {
		view.Unregister(counter.view)
	}
}

// getTracker returns a counterTracker for a specific time series defined by labelset.
// If `nil` is returned, it means that there are no aggregated counters that need to
// be incremented for this time series.
func (c *CounterAggregator) getTracker(lset labels.Labels) *counterTracker {
	var measures []*stats.Float64Measure
	for _, counter := range c.counters {
		if matchFiltersets(lset, counter.matchers) {
			measures = append(measures, counter.measure)
		}
	}
	if len(measures) == 0 {
		return nil
	}
	return &counterTracker{measures: measures, logger: c.logger}
}

// newPoint gets called on each new sample (timestamp, value) for time series that need to feed
// values into aggregated counters.
func (a *counterTracker) newPoint(ctx context.Context, lset labels.Labels, t int64, v float64) {
	if math.IsNaN(v) {
		level.Debug(a.logger).Log("msg", "got NaN value", "labels", lset, "last ts", a.lastTimestamp, "ts", t, "lastValue", a.lastValue)
		return
	}
	// Ignore points that are earlier than last seen timestamp.
	if t < a.lastTimestamp {
		level.Debug(a.logger).Log("msg", "out of order timestamp", "labels", lset, "last ts", a.lastTimestamp, "ts", t)
		return
	}
	// First time we're seeing a value; record it, but don't increment counters.
	if a.lastTimestamp == 0 {
		level.Debug(a.logger).Log("msg", "first point", "labels", lset)
		a.lastTimestamp = t
		a.lastValue = v
		return
	}
	var delta float64
	if v < a.lastValue {
		// Counter was reset.
		delta = v
		level.Debug(a.logger).Log("msg", "counter reset", "labels", lset, "value", v, "lastValue", a.lastValue, "delta", delta)
	} else {
		delta = v - a.lastValue
		level.Debug(a.logger).Log("msg", "got delta", "labels", lset, "value", v, "lastValue", a.lastValue, "delta", delta)
	}
	a.lastTimestamp = t
	a.lastValue = v
	if delta == 0 {
		return
	}
	ms := make([]stats.Measurement, len(a.measures))
	for i, measure := range a.measures {
		ms[i] = measure.M(delta)
	}
	statsRecord(ctx, ms...)
}
