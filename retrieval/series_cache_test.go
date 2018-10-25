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
	"time"

	"github.com/Stackdriver/stackdriver-prometheus-sidecar/targets"
	promlabels "github.com/prometheus/prometheus/pkg/labels"
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
	c := newSeriesCache(nil, dir, nil,
		targetMap{"/": &targets.Target{}},
		metadataMap{"//": &scrape.MetricMetadata{Type: textparse.MetricTypeGauge}},
		[]ResourceMap{
			{Type: "resource1", LabelMap: map[string]labelTranslation{}},
		},
		"", false,
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
		entry, ok, err := c.get(ctx, uint64(i))
		if !ok {
			t.Fatalf("entry with ref %d not found", i)
		}
		if err != nil {
			t.Fatalf("unexpected error: %s", err)
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
			if entry, ok, err := c.get(ctx, uint64(i)); ok {
				t.Fatalf("unexpected cache entry %d: %s", i, entry.lset)
			} else if err != nil {
				t.Fatalf("unexpected error: %s", err)
			}
		}
		// We should be able to read them all.
		for i := 3; i <= 7; i++ {
			entry, ok, err := c.get(ctx, uint64(i))
			if !ok {
				t.Fatalf("label set with ref %d not found", i)
			}
			if err != nil {
				t.Fatalf("unexpected error: %s", err)
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
			if entry, ok, err := c.get(ctx, uint64(i)); ok {
				t.Fatalf("unexpected cache entry %d: %s", i, entry.lset)
			} else if err != nil {
				t.Fatalf("unexpected error: %s", err)
			}
			continue
		}
		entry, ok, err := c.get(ctx, uint64(i))
		if !ok {
			t.Fatalf("entry with ref %d not found", i)
		}
		if err != nil {
			t.Fatalf("unexpected error: %s", err)
		}
		if !entry.lset.Equals(labels.FromStrings("a", strconv.Itoa(i))) {
			t.Fatalf("unexpected label set for ref %d: %s", i, entry.lset)
		}
	}
}

func TestSeriesCache_Refresh(t *testing.T) {
	resourceMaps := []ResourceMap{
		{
			Type:     "resource2",
			LabelMap: map[string]labelTranslation{"__resource_a": constValue("resource_a")},
		},
	}
	targetMap := targetMap{}
	metadataMap := metadataMap{}
	c := newSeriesCache(nil, "", nil, targetMap, metadataMap, resourceMaps, "", false)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Query unset reference.
	entry, ok, err := c.get(ctx, 1)
	if ok || entry != nil {
		t.Fatalf("unexpected series entry found: %v", entry)
	}
	if err != nil {
		t.Fatalf("unexpected error: %s", err)
	}

	// Set a series but the metadata and target getters won't have sufficient information for it.
	if err := c.set(ctx, 1, labels.FromStrings("__name__", "metric1", "job", "job1", "instance", "inst1"), 5); err != nil {
		t.Fatalf("unexpected error: %s", err)
	}
	// We should still not receive anything.
	entry, ok, err = c.get(ctx, 1)
	if ok || entry != nil {
		t.Fatalf("unexpected series entry found: %v", entry)
	}
	if err != nil {
		t.Fatalf("unexpected error: %s", err)
	}

	// Populate the getters with data.
	targetMap["job1/inst1"] = &targets.Target{
		Labels:           promlabels.FromStrings("job", "job1", "instance", "inst1"),
		DiscoveredLabels: promlabels.FromStrings("__resource_a", "resource2_a"),
	}
	metadataMap["job1/inst1/metric1"] = &scrape.MetricMetadata{Type: textparse.MetricTypeGauge, Metric: "metric1"}

	// Hack the timestamp of the last update to be sufficiently in the past that a refresh
	// will be triggered.
	c.entries[1].lastRefresh = time.Now().Add(-2 * refreshInterval)

	// Now another get should trigger a refresh, which now finds data.
	entry, ok, err = c.get(ctx, 1)
	if entry == nil || !ok || err != nil {
		t.Errorf("expected metadata but got none, error: %s", err)
	}
}

func TestSeriesCache_Filter(t *testing.T) {
	resourceMaps := []ResourceMap{
		{
			Type:     "resource2",
			LabelMap: map[string]labelTranslation{"__resource_a": constValue("resource_a")},
		},
	}
	// Populate the getters with data.
	targetMap := targetMap{
		"job1/inst1": &targets.Target{
			Labels:           promlabels.FromStrings("job", "job1", "instance", "inst1"),
			DiscoveredLabels: promlabels.FromStrings("__resource_a", "resource2_a"),
		},
	}
	metadataMap := metadataMap{
		"job1/inst1/metric1": &scrape.MetricMetadata{Type: textparse.MetricTypeGauge, Metric: "metric1"},
	}
	c := newSeriesCache(nil, "", []*promlabels.Matcher{
		&promlabels.Matcher{Type: promlabels.MatchEqual, Name: "a", Value: "a1"},
		&promlabels.Matcher{Type: promlabels.MatchEqual, Name: "b", Value: "b1"},
	}, targetMap, metadataMap, resourceMaps, "", false)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Test base case of metric that passes all filters. This primarily
	// ensures that our setup is correct and metrics aren't dropped for reasons
	// other than the filter.
	err := c.set(ctx, 1, labels.FromStrings("__name__", "metric1", "job", "job1", "instance", "inst1", "a", "a1", "b", "b1"), 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, err := c.get(ctx, 1); !ok || err != nil {
		t.Fatalf("metric not found: %s", err)
	}
	// Test filtered metric.
	err = c.set(ctx, 2, labels.FromStrings("__name__", "metric1", "job", "job1", "instance", "inst1", "a", "a1", "b", "b2"), 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, err := c.get(ctx, 2); err != nil {
		t.Fatalf("error retrieving metric: %s", err)
	} else if ok {
		t.Fatalf("metric was not filtered")
	}
}
