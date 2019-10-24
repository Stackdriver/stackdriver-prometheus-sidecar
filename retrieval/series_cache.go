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
	"sync"
	"time"

	"github.com/Stackdriver/stackdriver-prometheus-sidecar/metadata"
	"github.com/Stackdriver/stackdriver-prometheus-sidecar/targets"
	"github.com/go-kit/kit/log"
	"github.com/go-kit/kit/log/level"
	"github.com/pkg/errors"
	promlabels "github.com/prometheus/prometheus/pkg/labels"
	"github.com/prometheus/prometheus/pkg/textparse"
	"github.com/prometheus/tsdb"
	"github.com/prometheus/tsdb/labels"
	"github.com/prometheus/tsdb/wal"
	"go.opencensus.io/stats"
	"go.opencensus.io/stats/view"
	"go.opencensus.io/tag"
	metric_pb "google.golang.org/genproto/googleapis/api/metric"
	monitoredres_pb "google.golang.org/genproto/googleapis/api/monitoredres"
	monitoring_pb "google.golang.org/genproto/googleapis/monitoring/v3"
)

var (
	droppedSeries = stats.Int64("prometheus_sidecar/dropped_series",
		"Number of series that were dropped instead of being sent to Stackdriver", stats.UnitDimensionless)

	keyReason, _ = tag.NewKey("reason")
)

func init() {
	if err := view.Register(&view.View{
		Name:        "prometheus_sidecar/dropped_series",
		Description: "Number of series that were dropped instead of being sent to Stackdriver",
		Measure:     droppedSeries,
		TagKeys:     []tag.Key{keyReason},
		Aggregation: view.Count(),
	}); err != nil {
		panic(err)
	}
}

type seriesGetter interface {
	// Same interface as the standard map getter.
	get(ctx context.Context, ref uint64) (*seriesCacheEntry, bool, error)

	// Get the reset timestamp and adjusted value for the input sample.
	// If false is returned, the sample should be skipped.
	getResetAdjusted(ref uint64, t int64, v float64) (int64, float64, bool)

	// Attempt to set the new most recent time range for the series with given hash.
	// Returns false if it failed, in which case the sample must be discarded.
	updateSampleInterval(hash uint64, start, end int64) bool
}

// seriesCache holds a mapping from series reference to label set.
// It can garbage collect obsolete entries based on the most recent WAL checkpoint.
// Implements seriesGetter.
type seriesCache struct {
	logger            log.Logger
	dir               string
	filtersets        [][]*promlabels.Matcher
	targets           TargetGetter
	metadata          MetadataGetter
	counterAggregator *CounterAggregator
	resourceMaps      []ResourceMap
	metricsPrefix     string
	useGkeResource    bool
	renames           map[string]string

	// lastCheckpoint holds the index of the last checkpoint we garbage collected for.
	// We don't have to redo garbage collection until a higher checkpoint appears.
	lastCheckpoint int
	mtx            sync.Mutex
	// Map from series reference to various cached information about it.
	entries map[uint64]*seriesCacheEntry
	// Map from series hash to most recently written interval.
	intervals map[uint64]sampleInterval
}

type seriesCacheEntry struct {
	proto    *monitoring_pb.TimeSeries
	metadata *metadata.Entry
	lset     labels.Labels
	suffix   string
	hash     uint64

	hasReset       bool
	resetValue     float64
	resetTimestamp int64
	// maxSegment indicates the maximum WAL segment index in which
	// the series was first logged.
	// By providing it as an upper bound, we can safely delete a series entry
	// if the reference no longer appears in a checkpoint with an index at or above
	// this segment index.
	// We don't require a precise number since the caller may not be able to provide
	// it when retrieving records through a buffered reader.
	maxSegment int

	// Last time we attempted to populate meta information about the series.
	lastRefresh time.Time

	// Whether the series needs to be exported.
	exported bool

	// Counter tracker that will be called with each new sample if this time series
	// needs to feed data into aggregated counters.
	// This is nil if there are no aggregated counters that need to be incremented
	// for this time series.
	tracker *counterTracker
}

const refreshInterval = 3 * time.Minute

func (e *seriesCacheEntry) populated() bool {
	return e.proto != nil
}

func (e *seriesCacheEntry) shouldRefresh() bool {
	return !e.populated() && time.Since(e.lastRefresh) > refreshInterval
}

func newSeriesCache(
	logger log.Logger,
	dir string,
	filtersets [][]*promlabels.Matcher,
	renames map[string]string,
	targets TargetGetter,
	metadata MetadataGetter,
	resourceMaps []ResourceMap,
	metricsPrefix string,
	useGkeResource bool,
	counterAggregator *CounterAggregator,
) *seriesCache {
	if logger == nil {
		logger = log.NewNopLogger()
	}
	return &seriesCache{
		logger:            logger,
		dir:               dir,
		filtersets:        filtersets,
		targets:           targets,
		metadata:          metadata,
		resourceMaps:      resourceMaps,
		entries:           map[uint64]*seriesCacheEntry{},
		intervals:         map[uint64]sampleInterval{},
		metricsPrefix:     metricsPrefix,
		useGkeResource:    useGkeResource,
		renames:           renames,
		counterAggregator: counterAggregator,
	}
}

func (c *seriesCache) run(ctx context.Context) {
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			if err := c.garbageCollect(); err != nil {
				level.Error(c.logger).Log("msg", "garbage collection failed", "err", err)
			}
		}
	}
}

// garbageCollect drops obsolete cache entries based on the contents of the most
// recent checkpoint.
func (c *seriesCache) garbageCollect() error {
	cpDir, cpNum, err := tsdb.LastCheckpoint(c.dir)
	if errors.Cause(err) == tsdb.ErrNotFound {
		return nil // Nothing to do.
	}
	if err != nil {
		return errors.Wrap(err, "find last checkpoint")
	}
	if cpNum <= c.lastCheckpoint {
		return nil
	}
	sr, err := wal.NewSegmentsReader(cpDir)
	if err != nil {
		return errors.Wrap(err, "open segments")
	}
	defer sr.Close()

	// Scan all series records in the checkpoint and build a set of existing
	// references.
	var (
		r      = wal.NewReader(sr)
		exists = map[uint64]struct{}{}
		dec    tsdb.RecordDecoder
		series []tsdb.RefSeries
	)
	for r.Next() {
		rec := r.Record()
		if dec.Type(rec) != tsdb.RecordSeries {
			continue
		}
		series, err = dec.Series(rec, series[:0])
		if err != nil {
			return errors.Wrap(err, "decode series")
		}
		for _, s := range series {
			exists[s.Ref] = struct{}{}
		}
	}
	if r.Err() != nil {
		return errors.Wrap(err, "read checkpoint records")
	}

	// We can cleanup series in our cache that were neither in the current checkpoint nor
	// defined in WAL segments after the checkpoint.
	// References are monotonic but may be inserted into the WAL out of order. Thus we
	// consider the highest possible segment a series was created in.
	c.mtx.Lock()
	defer c.mtx.Unlock()

	for ref, entry := range c.entries {
		if _, ok := exists[ref]; !ok && entry.maxSegment <= cpNum {
			delete(c.entries, ref)
		}
	}
	c.lastCheckpoint = cpNum
	return nil
}

func (c *seriesCache) get(ctx context.Context, ref uint64) (*seriesCacheEntry, bool, error) {
	c.mtx.Lock()
	e, ok := c.entries[ref]
	c.mtx.Unlock()

	if !ok {
		return nil, false, nil
	}
	if e.shouldRefresh() {
		if err := c.refresh(ctx, ref); err != nil {
			return nil, false, err
		}
	}
	if !e.populated() {
		return nil, false, nil
	}
	return e, true, nil
}

// updateSampleInterval attempts to set the new most recent time range for the series with given hash.
// Returns false if it failed, in which case the sample must be discarded.
func (c *seriesCache) updateSampleInterval(hash uint64, start, end int64) bool {
	iv, ok := c.intervals[hash]
	if !ok || iv.accepts(start, end) {
		c.intervals[hash] = sampleInterval{start, end}
		return true
	}
	return false
}

type sampleInterval struct {
	start, end int64
}

func (si *sampleInterval) accepts(start, end int64) bool {
	return (start == si.start && end > si.end) || (start > si.start && start >= si.end)
}

// getResetAdjusted takes a sample for a referenced series and returns
// its reset timestamp and adjusted value.
// If the last return argument is false, the sample should be dropped.
func (c *seriesCache) getResetAdjusted(ref uint64, t int64, v float64) (int64, float64, bool) {
	c.mtx.Lock()
	e, ok := c.entries[ref]
	c.mtx.Unlock()
	if !ok {
		return 0, 0, false
	}
	hasReset := e.hasReset
	e.hasReset = true
	if !hasReset {
		e.resetTimestamp = t
		e.resetValue = v
		// If we just initialized the reset timestamp, this sample should be skipped.
		// We don't know the window over which the current cumulative value was built up over.
		// The next sample for will be considered from this point onwards.
		return 0, 0, false
	}
	if v < e.resetValue {
		// If the series was reset, set the reset timestamp to be one millisecond
		// before the timestamp of the current sample.
		// We don't know the true reset time but this ensures the range is non-zero
		// while unlikely to conflict with any previous sample.
		e.resetValue = 0
		e.resetTimestamp = t - 1
	}
	return e.resetTimestamp, v - e.resetValue, true
}

// set the label set for the given reference.
// maxSegment indicates the the highest segment at which the series was possibly defined.
func (c *seriesCache) set(ctx context.Context, ref uint64, lset labels.Labels, maxSegment int) error {
	exported := c.filtersets == nil || matchFiltersets(lset, c.filtersets)
	counterTracker := c.counterAggregator.getTracker(lset)

	if !exported && counterTracker == nil {
		return nil
	}

	c.mtx.Lock()
	c.entries[ref] = &seriesCacheEntry{
		maxSegment: maxSegment,
		lset:       lset,
		exported:   exported,
		tracker:    counterTracker,
	}
	c.mtx.Unlock()
	return c.refresh(ctx, ref)
}

func (c *seriesCache) refresh(ctx context.Context, ref uint64) error {
	c.mtx.Lock()
	entry := c.entries[ref]
	c.mtx.Unlock()

	entry.lastRefresh = time.Now()
	entryLabels := pkgLabels(entry.lset)

	// Probe for the target, its applicable resource, and the series metadata.
	// They will be used subsequently for all other Prometheus series that map to the same complex
	// Stackdriver series.
	// If either of those pieces of data is missing, the series will be skipped.
	target, err := c.targets.Get(ctx, entryLabels)
	if err != nil {
		return errors.Wrap(err, "retrieving target failed")
	}
	if target == nil {
		ctx, _ = tag.New(ctx, tag.Insert(keyReason, "target_not_found"))
		stats.Record(ctx, droppedSeries.M(1))
		level.Debug(c.logger).Log("msg", "target not found", "labels", entry.lset)
		return nil
	}
	resource, entryLabels, ok := c.getResource(target.DiscoveredLabels, entryLabels)
	if !ok {
		ctx, _ = tag.New(ctx, tag.Insert(keyReason, "unknown_resource"))
		stats.Record(ctx, droppedSeries.M(1))
		level.Debug(c.logger).Log("msg", "unknown resource", "labels", target.Labels, "discovered_labels", target.DiscoveredLabels)
		return nil
	}

	// Remove target labels and __name__ label.
	// Stackdriver only accepts a limited amount of labels, so we choose to economize aggressively here. This should be OK
	// because we expect that the target.Labels will be redundant with the Stackdriver MonitoredResource, which is derived
	// from the target Labels and DiscoveredLabels.
	finalLabels := targets.DropTargetLabels(entryLabels, target.Labels)
	for i, l := range finalLabels {
		if l.Name == "__name__" {
			finalLabels = append(finalLabels[:i], finalLabels[i+1:]...)
			break
		}
	}
	// Drop series with too many labels.
	if len(finalLabels) > maxLabelCount {
		ctx, _ = tag.New(ctx, tag.Insert(keyReason, "too_many_labels"))
		stats.Record(ctx, droppedSeries.M(1))
		level.Debug(c.logger).Log("msg", "too many labels", "labels", entry.lset)
		return nil
	}

	var (
		metricName     = entry.lset.Get("__name__")
		baseMetricName string
		suffix         string
		job            = entry.lset.Get("job")
		instance       = entry.lset.Get("instance")
	)
	metadata, err := c.metadata.Get(ctx, job, instance, metricName)
	if err != nil {
		return errors.Wrap(err, "get metadata")
	}
	if metadata == nil {
		// The full name didn't turn anything up. Check again in case it's a summary,
		// histogram, or counter without the metric name suffix.
		var ok bool
		if baseMetricName, suffix, ok = stripComplexMetricSuffix(metricName); ok {
			metadata, err = c.metadata.Get(ctx, job, instance, baseMetricName)
			if err != nil {
				return errors.Wrap(err, "get metadata")
			}
		}
		if metadata == nil {
			ctx, _ = tag.New(ctx, tag.Insert(keyReason, "metadata_not_found"))
			stats.Record(ctx, droppedSeries.M(1))
			level.Debug(c.logger).Log("msg", "metadata not found", "metric_name", metricName)
			return nil
		}
	}
	// Handle label modifications for histograms early so we don't build the label map twice.
	// We have to remove the 'le' label which defines the bucket boundary.
	if metadata.MetricType == textparse.MetricTypeHistogram {
		for i, l := range finalLabels {
			if l.Name == "le" {
				finalLabels = append(finalLabels[:i], finalLabels[i+1:]...)
				break
			}
		}
	}
	ts := &monitoring_pb.TimeSeries{
		Metric: &metric_pb.Metric{
			Type:   c.getMetricType(c.metricsPrefix, metricName),
			Labels: finalLabels.Map(),
		},
		Resource: resource,
	}

	switch metadata.MetricType {
	case textparse.MetricTypeCounter:
		ts.MetricKind = metric_pb.MetricDescriptor_CUMULATIVE
		ts.ValueType = metric_pb.MetricDescriptor_DOUBLE
		if metadata.ValueType != metric_pb.MetricDescriptor_VALUE_TYPE_UNSPECIFIED {
			ts.ValueType = metadata.ValueType
		}
		if baseMetricName != "" && suffix == metricSuffixTotal {
			ts.Metric.Type = c.getMetricType(c.metricsPrefix, baseMetricName)
		}
	case textparse.MetricTypeGauge, textparse.MetricTypeUnknown:
		ts.MetricKind = metric_pb.MetricDescriptor_GAUGE
		ts.ValueType = metric_pb.MetricDescriptor_DOUBLE
		if metadata.ValueType != metric_pb.MetricDescriptor_VALUE_TYPE_UNSPECIFIED {
			ts.ValueType = metadata.ValueType
		}
	case textparse.MetricTypeSummary:
		switch suffix {
		case metricSuffixSum:
			ts.MetricKind = metric_pb.MetricDescriptor_CUMULATIVE
			ts.ValueType = metric_pb.MetricDescriptor_DOUBLE
		case metricSuffixCount:
			ts.MetricKind = metric_pb.MetricDescriptor_CUMULATIVE
			ts.ValueType = metric_pb.MetricDescriptor_INT64
		case "": // Actual quantiles.
			ts.MetricKind = metric_pb.MetricDescriptor_GAUGE
			ts.ValueType = metric_pb.MetricDescriptor_DOUBLE
		default:
			return errors.Errorf("unexpected metric name suffix %q", suffix)
		}
	case textparse.MetricTypeHistogram:
		ts.Metric.Type = c.getMetricType(c.metricsPrefix, baseMetricName)
		ts.MetricKind = metric_pb.MetricDescriptor_CUMULATIVE
		ts.ValueType = metric_pb.MetricDescriptor_DISTRIBUTION
	default:
		return errors.Errorf("unexpected metric type %s", metadata.MetricType)
	}

	entry.proto = ts
	entry.metadata = metadata
	entry.suffix = suffix
	entry.hash = hashSeries(ts)

	return nil
}

func (c *seriesCache) getMetricType(prefix, name string) string {
	if repl, ok := c.renames[name]; ok {
		name = repl
	}
	return getMetricType(prefix, name)
}

// getResource returns the monitored resource, the entry labels, and whether the operation succeeded.
// The returned entry labels are a subset of `entryLabels` without the labels that were used as resource labels.
func (c *seriesCache) getResource(discovered, entryLabels promlabels.Labels) (*monitoredres_pb.MonitoredResource, promlabels.Labels, bool) {
	if c.useGkeResource {
		if lset, finalLabels := GKEResourceMap.BestEffortTranslate(discovered, entryLabels); lset != nil {
			return &monitoredres_pb.MonitoredResource{
				Type:   GKEResourceMap.Type,
				Labels: lset,
			}, finalLabels, true
		}
	}
	for _, m := range c.resourceMaps {
		if lset, finalLabels := m.Translate(discovered, entryLabels); lset != nil {
			return &monitoredres_pb.MonitoredResource{
				Type:   m.Type,
				Labels: lset,
			}, finalLabels, true
		}
	}
	return nil, entryLabels, false
}

// matchFiltersets checks whether any of the supplied filtersets passes.
func matchFiltersets(lset labels.Labels, filtersets [][]*promlabels.Matcher) bool {
	for _, fs := range filtersets {
		if matchFilterset(lset, fs) {
			return true
		}
	}
	return false
}

// matchFilterset checks whether labels match a given list of label matchers.
// All matchers need to match for the function to return true.
func matchFilterset(lset labels.Labels, filterset []*promlabels.Matcher) bool {
	for _, matcher := range filterset {
		if !matcher.Matches(lset.Get(matcher.Name)) {
			return false
		}
	}
	return true
}
