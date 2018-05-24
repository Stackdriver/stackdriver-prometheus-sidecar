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
	"errors"
	"fmt"
	"sort"

	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/model"
	"github.com/prometheus/prometheus/pkg/labels"
)

// The errors exposed.
var (
	ErrNotFound                    = errors.New("not found")
	ErrOutOfOrderSample            = errors.New("out of order sample")
	ErrDuplicateSampleForTimestamp = errors.New("duplicate sample for timestamp")
	ErrOutOfBounds                 = errors.New("out of bounds")
)

const (
	NoTimestamp = 0
)

type MetricFamily struct {
	*dto.MetricFamily
	// MetricResetTimestampMs must have one element for each
	// MetricFamily.Metric. Elements must be initialized to NoTimestamp if
	// the value is unknown.
	MetricResetTimestampMs []int64
	// see Target.DiscoveredLabels()
	TargetLabels labels.Labels
	// Extracted from the metric labels, preserved separately in case of relabelling.
	instanceLabel string
}

func (f *MetricFamily) String() string {
	return fmt.Sprintf("MetricFamily<dto.MetricFamily: %v MetricResetTimestampMs: %v TargetLabels: %v instanceLabel: \"%v\">",
		f.MetricFamily, f.MetricResetTimestampMs, f.TargetLabels, f.instanceLabel)
}

// SeparatorByte is a byte that cannot occur in valid UTF-8 sequences and is
// used to separate label names, label values, and other strings from each other
// when calculating their combined hash value (aka signature aka fingerprint).
const SeparatorByte byte = 255

// LabelPairsByName implements sort.Interface for []*MetricFamily based on the Name field.
type LabelPairsByName []*dto.LabelPair

func (f LabelPairsByName) Len() int           { return len(f) }
func (f LabelPairsByName) Swap(i, j int)      { f[i], f[j] = f[j], f[i] }
func (f LabelPairsByName) Less(i, j int) bool { return f[i].GetName() < f[j].GetName() }

func NewMetricFamily(pb *dto.MetricFamily, metricResetTimestampMs []int64, targetLabels labels.Labels) (*MetricFamily, error) {
	var instanceLabel string
LabelLoop:
	for _, metric := range pb.Metric {
		for _, label := range metric.GetLabel() {
			if label.GetName() == model.InstanceLabel {
				instanceLabel = label.GetValue()
				break LabelLoop
			}
		}
	}
	if len(instanceLabel) == 0 {
		return nil, fmt.Errorf("missing label '%s' in metric '%s'", model.InstanceLabel, pb.GetName())
	}
	return &MetricFamily{
		MetricFamily:           pb,
		MetricResetTimestampMs: metricResetTimestampMs,
		TargetLabels:           targetLabels,
		instanceLabel:          instanceLabel,
	}, nil
}

func (f *MetricFamily) Slice(i int) *MetricFamily {
	return &MetricFamily{
		MetricFamily: &dto.MetricFamily{
			Name:   f.Name,
			Help:   f.Help,
			Type:   f.Type,
			Metric: f.Metric[i : i+1],
		},
		MetricResetTimestampMs: f.MetricResetTimestampMs[i : i+1],
		TargetLabels:           f.TargetLabels,
		instanceLabel:          f.instanceLabel,
	}
}

func (f *MetricFamily) Fingerprint() uint64 {
	h := hashNew()
	h = hashAdd(h, f.GetName())
	h = hashAddByte(h, SeparatorByte)
	if len(f.instanceLabel) == 0 {
		panic("you must use NewMetricFamily to create instances of that type")
	}
	h = hashAdd(h, f.instanceLabel)
	for _, metric := range f.GetMetric() {
		sort.Sort(LabelPairsByName(metric.GetLabel()))
		for _, label := range metric.GetLabel() {
			h = hashAddByte(h, SeparatorByte)
			h = hashAdd(h, label.GetName())
			h = hashAddByte(h, SeparatorByte)
			h = hashAdd(h, label.GetValue())
		}
	}
	return h
}

type Appender interface {
	Add(metricFamily *MetricFamily) error
}
