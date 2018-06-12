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
	"fmt"
	"testing"

	"github.com/Stackdriver/stackdriver-prometheus-sidecar/targets"
	promlabels "github.com/prometheus/prometheus/pkg/labels"
	"github.com/prometheus/tsdb"
	"github.com/prometheus/tsdb/labels"
)

// Implements seriesGetter.
type seriesMap struct {
	m map[uint64]labels.Labels
}

func newSeriesMap() seriesMap {
	return seriesMap{m: make(map[uint64]labels.Labels)}
}

func (g *seriesMap) get(ref uint64) (labels.Labels, bool) {
	ls, ok := g.m[ref]
	return ls, ok
}

// Implements TargetGetter.
// The map key is the value of the first label in the lset given as an input to Get.
type targetMap struct {
	m map[string]targets.Target
}

func newTargetMap() targetMap {
	return targetMap{m: make(map[string]targets.Target)}
}

func (g *targetMap) Get(ctx context.Context, lset promlabels.Labels) (*targets.Target, error) {
	key := lset[0].Value
	t, ok := g.m[key]
	if !ok {
		return nil, fmt.Errorf("no target match for label %v", lset[0])
	}
	return &t, nil
}

func TestBuildSample(t *testing.T) {
	ctx := context.Background()
	seriesMap := newSeriesMap()
	targetMap := newTargetMap()

	timestamp := int64(1234)
	value := 2.1

	t.Run("NoSeries", func(t *testing.T) {
		recordSamples := []tsdb.RefSample{
			{Ref: /*unknown*/ 999, T: timestamp, V: value},
			{Ref: /*unknown*/ 999, T: timestamp, V: value},
		}
		sample, recordSamples, err := buildSample(ctx, &seriesMap, &targetMap, recordSamples)
		if err == nil {
			t.Errorf("Expected error, got sample %v", sample)
		}
		if len(recordSamples) != 1 {
			t.Errorf("Expected one leftover sample, got samples %v", recordSamples)
		}
	})

	t.Run("NoTarget", func(t *testing.T) {
		ref := uint64(0)
		seriesLabels := labels.Labels{{"__name__", "my_metric"}, {"job", "job1"}, {"instance", "i1"}}
		seriesMap.m[ref] = seriesLabels
		recordSamples := []tsdb.RefSample{{Ref: ref, T: timestamp, V: value}}
		sample, recordSamples, err := buildSample(ctx, &seriesMap, &targetMap, recordSamples)
		if err == nil {
			t.Errorf("Expected error, got sample %v", sample)
		}
		if len(recordSamples) != 0 {
			t.Errorf("Expected all samples to be consumed, got samples %v", recordSamples)
		}
	})

	t.Run("Successful", func(t *testing.T) {
		ref := uint64(0)
		seriesLabels := labels.Labels{{"__name__", "my_metric"}, {"job", "job1"}, {"instance", "i1"}}
		seriesMap.m[ref] = seriesLabels
		targetMap.m[seriesLabels[0].Value] = targets.Target{DiscoveredLabels: promlabels.Labels{{"dkey", "dvalue"}}}
		recordSamples := []tsdb.RefSample{{Ref: ref, T: timestamp, V: value}}
		sample, recordSamples, err := buildSample(ctx, &seriesMap, &targetMap, recordSamples)
		if err != nil {
			t.Error(err)
		}
		if len(recordSamples) != 0 {
			t.Errorf("Expected all samples to be consumed, got samples %v", recordSamples)
		}
		if sample == nil {
			t.Error("Unexpected nil sample")
		}
		if sample.GetName() != "my_metric" {
			t.Errorf("Expected name 'my_metric', got %v", sample.GetName())
		}
		if sample.Metric[0].GetTimestampMs() != timestamp {
			t.Errorf("Expected timestamp '%v', got %v", timestamp, sample.Metric[0].GetTimestampMs())
		}
		if sample.Metric[0].Untyped.GetValue() != value {
			t.Errorf("Expected value '%v', got %v", value, sample.Metric[0].Untyped.GetValue())
		}
		targetLabels := promlabels.FromStrings("dkey", "dvalue")
		if !promlabels.Equal(sample.TargetLabels, targetLabels) {
			t.Errorf("Expected target labels '%v', got %v", targetLabels, sample.TargetLabels)
		}
	})
}
