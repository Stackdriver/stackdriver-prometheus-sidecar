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
	"fmt"
	"math"

	"github.com/go-kit/kit/log"
	"github.com/go-kit/kit/log/level"
	promlabels "github.com/prometheus/prometheus/pkg/labels"
	"github.com/prometheus/tsdb/labels"
	"go.opencensus.io/stats"
	"go.opencensus.io/stats/view"
)

// CounterAggregator provides the 'aggregated counters' feature of the sidecar.
// It can be used to export a sum of multiple counters from Prometheus to
// Stackdriver as a single cumulative metric.
// Each aggregated counter is associated with a single OpenCensus counter that
// can then be exported to Stackdriver (as a CUMULATIVE metric) or exposed to
// Prometheus via the standard `/metrics` endpoint. Regular flushing of counter
// values is implemented by OpenCensus.
type CounterAggregator struct {
	logger      log.Logger
	counters    []*aggregatedCounter
	statsRecord func(context.Context, ...stats.Measurement) // used in testing.
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

func (a CounterAggregatorMetricConfig) Equal(b CounterAggregatorMetricConfig) bool {
	return a.Help == b.Help &&
		fmt.Sprintf("%v", a.Matchers) == fmt.Sprintf("%v", b.Matchers)
}

// counterTracker keeps track of a single time series that has at least one aggregated
// counter associated with it (i.e. there is at least one aggregated counter that has
// Matchers covering this time series). Last timestamp and value are tracked
// to detect counter resets.
type counterTracker struct {
	lastTimestamp int64
	lastValue     float64
	measures      []*stats.Float64Measure
	ca            *CounterAggregator
}

// NewCounterAggregator creates a counter aggregator.
func NewCounterAggregator(logger log.Logger, config *CounterAggregatorConfig) (*CounterAggregator, error) {
	aggregator := &CounterAggregator{logger: logger, statsRecord: stats.Record}
	for metric, cfg := range *config {
		measure := stats.Float64(metric, cfg.Help, stats.UnitDimensionless)
		v := &view.View{
			Name:        metric,
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
	return &counterTracker{measures: measures, ca: c}
}

// newPoint gets called on each new sample (timestamp, value) for time series that need to feed
// values into aggregated counters.
func (t *counterTracker) newPoint(ctx context.Context, lset labels.Labels, ts int64, v float64) {
	if math.IsNaN(v) {
		level.Debug(t.ca.logger).Log("msg", "got NaN value", "labels", lset, "last ts", t.lastTimestamp, "ts", t, "lastValue", t.lastValue)
		return
	}
	// Ignore measurements that are earlier than last seen timestamp, since they are already covered by
	// later values. Samples are coming from TSDB in order, so this is unlikely to happen.
	if ts < t.lastTimestamp {
		level.Debug(t.ca.logger).Log("msg", "out of order timestamp", "labels", lset, "last ts", t.lastTimestamp, "ts", ts)
		return
	}
	// Use the first value we see as the starting point for the counter.
	if t.lastTimestamp == 0 {
		level.Debug(t.ca.logger).Log("msg", "first point", "labels", lset)
		t.lastTimestamp = ts
		t.lastValue = v
		return
	}
	var delta float64
	if v < t.lastValue {
		// Counter was reset.
		delta = v
		level.Debug(t.ca.logger).Log("msg", "counter reset", "labels", lset, "value", v, "lastValue", t.lastValue, "delta", delta)
	} else {
		delta = v - t.lastValue
		level.Debug(t.ca.logger).Log("msg", "got delta", "labels", lset, "value", v, "lastValue", t.lastValue, "delta", delta)
	}
	t.lastTimestamp = ts
	t.lastValue = v
	if delta == 0 {
		return
	}
	ms := make([]stats.Measurement, len(t.measures))
	for i, measure := range t.measures {
		ms[i] = measure.M(delta)
	}
	t.ca.statsRecord(ctx, ms...)
}
