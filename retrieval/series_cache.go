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
	"path/filepath"
	"sync"
	"time"

	"github.com/go-kit/kit/log"
	"github.com/go-kit/kit/log/level"
	"github.com/pkg/errors"
	"github.com/prometheus/tsdb"
	"github.com/prometheus/tsdb/labels"
	"github.com/prometheus/tsdb/wal"
)

type seriesGetter interface {
	// Same interface as the standard map getter.
	getLabels(ref uint64) (labels.Labels, bool)

	// Get the reset timestamp and adjusted value for the input sample.
	// If false is returned, the sample should be skipped.
	getResetAdjusted(ref uint64, t int64, v float64) (int64, float64, bool)
}

// seriesCache holds a mapping from series reference to label set.
// It can garbage collect obsolete entries based on the most recent WAL checkpoint.
// Implements seriesGetter.
type seriesCache struct {
	logger log.Logger
	dir    string

	// lastCheckpoint holds the index of the last checkpoint we garbage collected for.
	// We don't have to redo garbage collection until a higher checkpoint appears.
	lastCheckpoint int
	mtx            sync.Mutex
	entries        map[uint64]*seriesCacheEntry
}

type seriesCacheEntry struct {
	lset           labels.Labels
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
}

func newSeriesCache(logger log.Logger, dir string) *seriesCache {
	if logger == nil {
		logger = log.NewNopLogger()
	}
	return &seriesCache{
		logger:  logger,
		dir:     dir,
		entries: map[uint64]*seriesCacheEntry{},
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
	if err != nil {
		return errors.Wrap(err, "find last checkpoint")
	}
	if cpNum <= c.lastCheckpoint {
		return nil
	}
	sr, err := wal.NewSegmentsReader(filepath.Join(c.dir, cpDir))
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

func (c *seriesCache) getLabels(ref uint64) (labels.Labels, bool) {
	c.mtx.Lock()
	e, ok := c.entries[ref]
	c.mtx.Unlock()
	if !ok {
		return nil, false
	}
	return e.lset, true
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
	init := !e.hasReset
	e.hasReset = true
	if init {
		e.resetTimestamp = t
		e.resetValue = v
		// If we just initialized the reset timestamp, this sample should be skipped.
		// We don't know the window over which the current cumulative value was built up over.
		// The next sample for will be considered from this point onwards.
		return 0, 0, false
	}
	if v < e.resetValue {
		// If the series was reset, place the reset timestamp right before
		// the currently observed timestamp.
		// We don't know the true reset time but the range between the two
		// must be greater than zero.
		e.resetValue = 0
		e.resetTimestamp = t - 1
	}
	return e.resetTimestamp, v - e.resetValue, true
}

// set the label set for the given reference.
// maxSegment indicates the the highest segment at which the series was possibly defined.
func (c *seriesCache) set(ref uint64, lset labels.Labels, maxSegment int) {
	c.mtx.Lock()
	c.entries[ref] = &seriesCacheEntry{maxSegment: maxSegment, lset: lset}
	c.mtx.Unlock()
}
