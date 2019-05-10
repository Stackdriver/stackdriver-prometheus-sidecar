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
	"bytes"
	"context"
	"math"
	"reflect"
	"testing"

	"github.com/go-kit/kit/log"
	promlabels "github.com/prometheus/prometheus/pkg/labels"
	"github.com/prometheus/tsdb/labels"
	"go.opencensus.io/stats"
)

func TestCounterAggregator(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	type input struct {
		ts    int64
		value float64
	}

	for _, tt := range []struct {
		name  string
		input []input
		want  []float64
	}{
		{"simple", []input{input{1, 15}, input{2, 25}}, []float64{10}},
		{"counter resets", []input{input{1, 15}, input{2, 25}, input{3, 5}, input{4, 25}, input{5, 15}}, []float64{10, 5, 20, 15}},
		{"NaNs are ignored", []input{input{1, 15}, input{2, math.NaN()}, input{3, 25}}, []float64{10}},
		{"out of order points are ignored", []input{input{1, 15}, input{3, 25}, {2, 20}, {4, 30}}, []float64{10, 5}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			logBuffer := &bytes.Buffer{}
			defer func() {
				if logBuffer.Len() > 0 {
					t.Log(logBuffer.String())
				}
			}()
			logger := log.NewLogfmtLogger(logBuffer)

			aggr, _ := NewCounterAggregator(logger, &CounterAggregatorConfig{
				"counter1": &CounterAggregatorMetricConfig{Matchers: [][]*promlabels.Matcher{
					{&promlabels.Matcher{Type: promlabels.MatchEqual, Name: "a", Value: "a1"}},
				}},
			})
			defer aggr.Close()

			var got []float64
			aggr.statsRecord = func(ctx context.Context, ms ...stats.Measurement) {
				for _, m := range ms {
					got = append(got, m.Value())
				}
			}

			lset := labels.FromStrings("__name__", "metric1", "job", "job1", "instance", "inst1", "a", "a1")
			tracker := aggr.getTracker(lset)

			for _, input := range tt.input {
				tracker.newPoint(ctx, lset, input.ts, input.value)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("unexpected values %v; want %v", got, tt.want)
			}
		})
	}
}
