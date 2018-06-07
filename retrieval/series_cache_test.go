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
	"io/ioutil"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/prometheus/tsdb"
	"github.com/prometheus/tsdb/labels"
	"github.com/prometheus/tsdb/wal"
)

func TestScrapeCache(t *testing.T) {
	dir, err := ioutil.TempDir("", "scrape_cache")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	c := newSeriesCache(nil, dir)

	// Add the series to the cache. Normally, they would have been inserted during
	// tailing - either with the checkpoint index or a segment index at or below.
	c.set(1, labels.FromStrings("a", "1"), 0)
	c.set(2, labels.FromStrings("a", "2"), 5)
	c.set(3, labels.FromStrings("a", "3"), 9)
	c.set(4, labels.FromStrings("a", "4"), 10)
	c.set(5, labels.FromStrings("a", "5"), 11)
	c.set(6, labels.FromStrings("a", "6"), 12)
	c.set(7, labels.FromStrings("a", "7"), 13)

	// We should be able to read them all.
	for i := 1; i <= 7; i++ {
		lset, ok := c.get(uint64(i))
		if !ok {
			t.Fatalf("label set with ref %d not found", i)
		}
		if !lset.Equals(labels.FromStrings("a", strconv.Itoa(i))) {
			t.Fatalf("unexpecte3d label set for ref %d: %s", i, lset)
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
			if lset, ok := c.get(uint64(i)); ok {
				t.Fatalf("unexpected cache entry %d: %s", i, lset)
			}
		}
		// We should be able to read them all.
		for i := 3; i <= 7; i++ {
			lset, ok := c.get(uint64(i))
			if !ok {
				t.Fatalf("label set with ref %d not found", i)
			}
			if !lset.Equals(labels.FromStrings("a", strconv.Itoa(i))) {
				t.Fatalf("unexpecte3d label set for ref %d: %s", i, lset)
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
			if lset, ok := c.get(uint64(i)); ok {
				t.Fatalf("unexpected cache entry %d: %s", i, lset)
			}
			continue
		}
		lset, ok := c.get(uint64(i))
		if !ok {
			t.Fatalf("label set with ref %d not found", i)
		}
		if !lset.Equals(labels.FromStrings("a", strconv.Itoa(i))) {
			t.Fatalf("unexpecte3d label set for ref %d: %s", i, lset)
		}
	}
}
