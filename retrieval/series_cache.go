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

// seriesCache holds a mapping from series reference to label set.
// It can garbage collect obsolete entries based on the most recent WAL checkpoint.
type seriesCache struct {
	logger         log.Logger
	dir            string
	lastCheckpoint int
	mtx            sync.Mutex
	lsets          map[uint64]seriesCacheEntry
}

type seriesCacheEntry struct {
	lset       labels.Labels
	maxSegment int
}

func newSeriesCache(logger log.Logger, dir string) *seriesCache {
	if logger == nil {
		logger = log.NewNopLogger()
	}
	return &seriesCache{
		logger: logger,
		dir:    dir,
		lsets:  map[uint64]seriesCacheEntry{},
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

	for r, e := range c.lsets {
		if _, ok := exists[r]; !ok && e.maxSegment <= cpNum {
			delete(c.lsets, r)
		}
	}
	c.lastCheckpoint = cpNum
	return nil
}

func (c *seriesCache) get(ref uint64) (labels.Labels, bool) {
	c.mtx.Lock()
	l, ok := c.lsets[ref]
	c.mtx.Unlock()
	return l.lset, ok
}

// set the label set for the given reference.
// maxSegment indicates the the highest segment at which the series was possibly defined.
func (c *seriesCache) set(ref uint64, lset labels.Labels, maxSegment int) {
	c.mtx.Lock()
	c.lsets[ref] = seriesCacheEntry{maxSegment: maxSegment, lset: lset}
	c.mtx.Unlock()
}
