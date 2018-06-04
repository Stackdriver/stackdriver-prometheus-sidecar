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
	"sort"

	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/prometheus/pkg/labels"
)

func LabelPairsToLabels(input []*dto.LabelPair) (output labels.Labels) {
	output = make(labels.Labels, 0, len(input))
	for _, label := range input {
		output = append(output, labels.Label{
			Name:  label.GetName(),
			Value: label.GetValue(),
		})
	}
	// Some code expect these to be in order.
	sort.Sort(output)
	return
}

func LabelsToLabelPairs(lset labels.Labels) []*dto.LabelPair {
	if len(lset) == 0 {
		return nil
	}
	labelPairs := make([]*dto.LabelPair, len(lset))
	for i := range lset {
		labelPairs[i] = &dto.LabelPair{
			Name:  &lset[i].Name,
			Value: &lset[i].Value,
		}
	}
	return labelPairs
}
