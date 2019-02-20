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

package targets

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"testing"

	"github.com/prometheus/prometheus/pkg/labels"
)

func TestTargetCache_Error(t *testing.T) {
	var handler http.HandlerFunc

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handler(w, r)
	}))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	u, err := url.Parse(ts.URL)
	if err != nil {
		t.Fatal(err)
	}
	c := NewCache(nil, nil, u)

	expectedTarget := &Target{
		Labels: labels.FromStrings("job", "a", "instance", "c"),
	}
	// Initialize cache with expected target.
	c.targets[cacheKey("a", "c")] = []*Target{expectedTarget}

	for i, hf := range []http.HandlerFunc{
		// Return HTTP error.
		func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusServiceUnavailable)
		},
		// Malformed JSON.
		func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte(`{"`))
		},
		// Application error,
		func(w http.ResponseWriter, r *http.Request) {
			json.NewEncoder(w).Encode(apiResponse{
				Status:    "error",
				ErrorType: "bad_data",
				Error:     "some data is bad",
			})
		},
	} {
		handler = hf

		target, err := c.Get(ctx, labels.FromStrings("job", "a", "instance", "b"))
		if target != nil {
			t.Fatalf("%d: unexpected target %s", i, target.Labels)
		}
		if err == nil {
			t.Fatalf("%d: expected error but got none", i)
		}
		// Basic check that cache entries don't get purged during errors.
		target, err = c.Get(ctx, labels.FromStrings("job", "a", "instance", "c"))
		if err != nil {
			t.Fatalf("%d: unexpected error: %s", i, err)
		}
		if !reflect.DeepEqual(target, expectedTarget) {
			t.Fatalf("%d: unexepected target %s", i, target)
		}
	}
}

func TestTargetCache_Success(t *testing.T) {
	var handler func() []*Target

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var resp apiResponse
		resp.Status = "success"
		resp.Data.ActiveTargets = handler()
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			t.Fatal(err)
		}
	}))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	u, err := url.Parse(ts.URL)
	if err != nil {
		t.Fatal(err)
	}
	c := NewCache(nil, nil, u)

	handler = func() []*Target {
		return []*Target{
			{
				Labels:           labels.FromStrings("job", "job1", "instance", "instance1"),
				DiscoveredLabels: labels.FromStrings("__job", "job1", "__instance", "instance1"),
			},
			{Labels: labels.FromStrings("job", "job1", "instance", "instance2")},
			// Two targets with the same job/instance combination
			{Labels: labels.FromStrings("job", "job2", "instance", "instance1", "port", "port1")},
			{Labels: labels.FromStrings("job", "job2", "instance", "instance1", "port", "port2")},
		}
	}
	target1, err := c.Get(ctx, labels.FromStrings("__name__", "metric1", "job", "job1", "instance", "instance1"))
	if err != nil {
		t.Fatal(err)
	}
	if !labelsEqual(target1.Labels, labels.FromStrings("job", "job1", "instance", "instance1")) {
		t.Fatalf("unexpected target labels %s", target1.Labels)
	}
	if !labelsEqual(target1.DiscoveredLabels, labels.FromStrings("__job", "job1", "__instance", "instance1")) {
		t.Fatalf("unexpected discovered target labels %s", target1.DiscoveredLabels)
	}
	// Get a non-existant target. The first time it should attempt a refresh.
	target, err := c.Get(ctx, labels.FromStrings("__name__", "metric1", "job", "absent", "instance", "absent"))
	if err != nil {
		t.Fatal(err)
	}
	if target != nil {
		t.Fatalf("unexpected target %s", target.Labels)
	}
	// The first target we queried should be the same heap object after refresh.
	target, err = c.Get(ctx, labels.FromStrings("__name__", "metric1", "job", "job1", "instance", "instance1"))
	if err != nil {
		t.Fatal(err)
	}
	if target != target1 {
		t.Fatalf("target was not the same heap object")
	}

	handler = func() []*Target {
		t.Fatal("unexpected request")
		return nil
	}
	// Get absent target from above again. This time it must not do a request.
	target, err = c.Get(ctx, labels.FromStrings("__name__", "metric1", "job", "absent", "instance", "absent"))
	if err != nil {
		t.Fatal(err)
	}
	if target != nil {
		t.Fatalf("unexpected target %s", target.Labels)
	}
	// Get targets that have been retrieved through a previous refresh.
	// This must not trigger refreshs.
	target, err = c.Get(ctx, labels.FromStrings("__name__", "metric1", "job", "job1", "instance", "instance2"))
	if err != nil {
		t.Fatal(err)
	}
	if !labelsEqual(target.Labels, labels.FromStrings("job", "job1", "instance", "instance2")) {
		t.Fatalf("unexpected target labels %s", target.Labels)
	}
	// Get the targets with the same job/instance but differing ports.
	target, err = c.Get(ctx, labels.FromStrings("__name__", "metric1", "job", "job2", "instance", "instance1", "port", "port1"))
	if err != nil {
		t.Fatal(err)
	}
	if !labelsEqual(target.Labels, labels.FromStrings("job", "job2", "instance", "instance1", "port", "port1")) {
		t.Fatalf("unexpected target labels %s", target.Labels)
	}
	target, err = c.Get(ctx, labels.FromStrings("__name__", "metric1", "job", "job2", "instance", "instance1", "port", "port2"))
	if err != nil {
		t.Fatal(err)
	}
	if !labelsEqual(target.Labels, labels.FromStrings("job", "job2", "instance", "instance1", "port", "port2")) {
		t.Fatalf("unexpected target labels %s", target.Labels)
	}

	// Set a new response, which should invalidate previous targets.
	handler = func() []*Target {
		return []*Target{
			{Labels: labels.FromStrings("job", "job3", "instance", "instance1")},
		}
	}
	target, err = c.Get(ctx, labels.FromStrings("__name__", "metric1", "job", "job3", "instance", "instance1", "a", "a1"))
	if err != nil {
		t.Fatal(err)
	}
	if !labelsEqual(target.Labels, labels.FromStrings("job", "job3", "instance", "instance1")) {
		t.Fatalf("unexpected target labels %s", target.Labels)
	}
	// Old targets should be gone.
	target, err = c.Get(ctx, labels.FromStrings("__name__", "metric1", "job", "job1", "instance", "instance1"))
	if err != nil {
		t.Fatal(err)
	}
	if target != nil {
		t.Fatalf("unexpected target %s", target.Labels)
	}
}

func TestTargetCache_EmptyEntry(t *testing.T) {
	var handler func() []*Target

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var resp apiResponse
		resp.Status = "success"
		resp.Data.ActiveTargets = handler()
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			t.Fatal(err)
		}
	}))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	u, err := url.Parse(ts.URL)
	if err != nil {
		t.Fatal(err)
	}

	c := NewCache(nil, nil, u)
	// Initialize cache with negative-cached target.
	c.targets[cacheKey("job1", "instance-not-exists")] = nil

	// No target in initial response.
	handler = func() []*Target {
		return []*Target{}
	}
	c.Get(ctx, labels.FromStrings("__name__", "metric1", "job", "job1", "instance", "instance1"))

	// Empty entry should be kept in cache.
	val, ok := c.targets[cacheKey("job1", "instance-not-exists")]
	if !ok {
		t.Fatalf("Negative cache should be kept.")
	}
	if val != nil {
		t.Fatalf("Unexpected value job1/instance-not-exists: %v", val)
	}

	// Create a new empty entry by querying job/instance pair not available.
	c.Get(ctx, labels.FromStrings("__name__", "metric2", "job", "job2", "instance", "instance-not-exists"))
	val, ok = c.targets[cacheKey("job2", "instance-not-exists")]
	if !ok {
		t.Fatalf("Negative cache should be kept.")
	}
	if val != nil {
		t.Fatalf("Unexpected value job2/instance-not-exists: %v", val)
	}

	// Add a new instance into response, which will trigger replacing cache.
	handler = func() []*Target {
		return []*Target{
			{Labels: labels.FromStrings("job", "job1", "instance", "instance1")},
		}
	}
	c.Get(ctx, labels.FromStrings("__name__", "metric1", "job", "job1", "instance", "instance1"))

	// Empty entry created should be kept in cache.
	val, ok = c.targets[cacheKey("job2", "instance-not-exists")]
	if !ok {
		t.Fatalf("Negative cache should be kept.")
	}
	if val != nil {
		t.Fatalf("Unexpected value job2/instance-not-exists: %v", val)
	}
}

func TestDropTargetLabels(t *testing.T) {
	cases := []struct {
		series, target, result labels.Labels
	}{
		// Normal case.
		{
			series: labels.FromStrings("__name__", "metric", "job", "job1", "instance", "instance1", "a", "b"),
			target: labels.FromStrings("job", "job1", "instance", "instance1"),
			result: labels.FromStrings("__name__", "metric", "a", "b"),
		},
		// Same label but different values
		{
			series: labels.FromStrings("a", "a1", "b", "b1", "c", "c1"),
			target: labels.FromStrings("a", "a1", "b", "b2"),
			result: labels.FromStrings("b", "b1", "c", "c1"),
		},
		// Target label absent from series.
		{
			series: labels.FromStrings("b", "b1", "c", "c1"),
			target: labels.FromStrings("a", "a1", "b", "b1"),
			result: labels.FromStrings("c", "c1"),
		},
	}
	for _, c := range cases {
		if got := DropTargetLabels(c.series, c.target); !labelsEqual(got, c.result) {
			t.Fatalf("expected %s for series %s and target %s but got %s", c.result, c.series, c.target, got)
		}
	}
}
