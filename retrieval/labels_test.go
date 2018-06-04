// Copyright 2016 The Prometheus Authors
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package retrieval

import (
	"reflect"
	"testing"

	"github.com/prometheus/prometheus/pkg/labels"
)

func TestLabelConversion(t *testing.T) {
	originalLset := labels.FromMap(map[string]string{
		"label1": "1:foo oaeu aoeu aoeu aoeu ou",
		"label2": "2:foo oaeu aoeu aoeu aoeu ou",
		"label3": "3:foo oaeu aoeu aoeu aoeu ou",
		"label4": "4:foo oaeu aoeu aoeu aoeu ou",
		"label5": "5:foo oaeu aoeu aoeu aoeu ou",
	})
	labelPairs := LabelsToLabelPairs(originalLset)
	// Move label arounds to ensure the result is sorted.
	labelPairs[0], labelPairs[1] = labelPairs[1], labelPairs[0]
	lset := LabelPairsToLabels(labelPairs)
	if !reflect.DeepEqual(originalLset, lset) {
		t.Fatalf("labels not as expected.\nWanted: %+v\nGot:    %+v", originalLset, lset)
	}
}

func BenchmarkLabelsToLabelPairs(b *testing.B) {
	b.ReportAllocs()
	lset := labels.FromMap(map[string]string{
		"label1": "1:foo oaeu aoeu aoeu aoeu ou",
		"label2": "2:foo oaeu aoeu aoeu aoeu ou",
		"label3": "3:foo oaeu aoeu aoeu aoeu ou",
		"label4": "4:foo oaeu aoeu aoeu aoeu ou",
		"label5": "5:foo oaeu aoeu aoeu aoeu ou",
	})
	for i := 0; i < b.N; i++ {
		LabelsToLabelPairs(lset)
	}
}
