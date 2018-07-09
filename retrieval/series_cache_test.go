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
	"io/ioutil"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/Stackdriver/stackdriver-prometheus-sidecar/targets"
	"github.com/prometheus/prometheus/pkg/textparse"
	"github.com/prometheus/prometheus/scrape"
	"github.com/prometheus/tsdb"
	"github.com/prometheus/tsdb/labels"
	"github.com/prometheus/tsdb/wal"
)

// This test primarily verifies the garbage collection logic of the cache.
// The getters are verified integrated into the sample builder in transform_test.go
func TestScrapeCache_GarbageCollect(t *testing.T) {
	dir, err := ioutil.TempDir("", "scrape_cache")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	// Initialize the series cache with "empty" target and metadata maps.
	// The series we use below have no job, instance, and __name__ labels set, which
	// will result in those empty lookup keys.
	c := newSeriesCache(nil, dir,
		targetMap{"/": &targets.Target{}},
		metadataMap{"//": &scrape.MetricMetadata{Type: textparse.MetricTypeGauge}},
		[]ResourceMap{
			{Type: "resource1", LabelMap: map[string]string{}},
		},
	)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Add the series to the cache. Normally, they would have been inserted during
	// tailing - either with the checkpoint index or a segment index at or below.
	c.set(ctx, 1, labels.FromStrings("a", "1"), 0)
	c.set(ctx, 2, labels.FromStrings("a", "2"), 5)
	c.set(ctx, 3, labels.FromStrings("a", "3"), 9)
	c.set(ctx, 4, labels.FromStrings("a", "4"), 10)
	c.set(ctx, 5, labels.FromStrings("a", "5"), 11)
	c.set(ctx, 6, labels.FromStrings("a", "6"), 12)
	c.set(ctx, 7, labels.FromStrings("a", "7"), 13)

	// We should be able to read them all.
	for i := 1; i <= 7; i++ {
		entry, ok := c.get(uint64(i))
		if !ok {
			t.Fatalf("entry with ref %d not found", i)
		}
		if !entry.lset.Equals(labels.FromStrings("a", strconv.Itoa(i))) {
			t.Fatalf("unexpected label set for ref %d: %s", i, entry.lset)
		}
	}

	// Create a checkpoint with index 10, in which some old series were dropped.
	cp10dir := filepath.Join(dir, "checkpoint.00010")
	{
		series := []tsdb.RefSeries{
			{Ref: 3, Labels: labels.FromStrings("a", "3")},
			{Ref: 4, Labels: labels.FromStrings("a", "4")},
		}
		w, err := wal.New(nil, nil, cp10dir)
		if err != nil {
			t.Fatal(err)
		}
		var enc tsdb.RecordEncoder
		if err := w.Log(enc.Series(series, nil)); err != nil {
			t.Fatal(err)
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
	}

	// Run garbage collection. Afterwards the first two series should be gone.
	// Put in a loop to verify that garbage collection is idempotent.
	for i := 0; i < 10; i++ {
		if err := c.garbageCollect(); err != nil {
			t.Fatal(err)
		}
		for i := 1; i < 2; i++ {
			if entry, ok := c.get(uint64(i)); ok {
				t.Fatalf("unexpected cache entry %d: %s", i, entry.lset)
			}
		}
		// We should be able to read them all.
		for i := 3; i <= 7; i++ {
			entry, ok := c.get(uint64(i))
			if !ok {
				t.Fatalf("label set with ref %d not found", i)
			}
			if !entry.lset.Equals(labels.FromStrings("a", strconv.Itoa(i))) {
				t.Fatalf("unexpected label set for ref %d: %s", i, entry.lset)
			}
		}
	}

	// We create a higher checkpoint, which garbageCollect should detect on its next call.
	cp12dir := filepath.Join(dir, "checkpoint.00012")
	{
		series := []tsdb.RefSeries{
			{Ref: 4, Labels: labels.FromStrings("a", "4")},
		}
		w, err := wal.New(nil, nil, cp12dir)
		if err != nil {
			t.Fatal(err)
		}
		var enc tsdb.RecordEncoder
		if err := w.Log(enc.Series(series, nil)); err != nil {
			t.Fatal(err)
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.RemoveAll(cp10dir); err != nil {
		t.Fatal(err)
	}
	if err := c.garbageCollect(); err != nil {
		t.Fatal(err)
	}
	//  Only series 4 and 7 should be left.
	for i := 1; i <= 7; i++ {
		if i != 4 && i != 7 {
			if entry, ok := c.get(uint64(i)); ok {
				t.Fatalf("unexpected cache entry %d: %s", i, entry.lset)
			}
			continue
		}
		entry, ok := c.get(uint64(i))
		if !ok {
			t.Fatalf("entrywith ref %d not found", i)
		}
		if !entry.lset.Equals(labels.FromStrings("a", strconv.Itoa(i))) {
			t.Fatalf("unexpected label set for ref %d: %s", i, entry.lset)
		}
	}
}
