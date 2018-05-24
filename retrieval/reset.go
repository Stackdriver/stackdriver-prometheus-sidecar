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
	"fmt"
	"hash/fnv"
	"time"

	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/prometheus/pkg/labels"
)

type ResetPointKey uint64

type PointHistogramBucket struct {
	CumulativeCount uint64
	UpperBound      float64
}

type PointHistogram struct {
	Count  uint64
	Sum    float64
	Bucket []PointHistogramBucket
}

type PointSummary struct {
	Count uint64
	Sum   float64
}

type PointValue struct {
	// Only one of the following fields may be set.
	Counter   float64
	Histogram PointHistogram
	Summary   PointSummary
}

type Point struct {
	Timestamp  time.Time
	ResetValue *PointValue
	LastValue  *PointValue
}

type resetPointMapper interface {
	GetResetPoint(key ResetPointKey) (point *Point)
	AddResetPoint(key ResetPointKey, point Point)
	HasResetPoints() bool
}

func ResetPointCompatible(mType dto.MetricType) bool {
	return mType == dto.MetricType_COUNTER || mType == dto.MetricType_HISTOGRAM || mType == dto.MetricType_SUMMARY
}

func NewResetPointKey(metricName string, metricLabels labels.Labels, metricType dto.MetricType) (key ResetPointKey) {
	if !ResetPointCompatible(metricType) {
		return 0 // TODO(jkohen): turn into an error?
	}
	h := fnv.New64a()
	h.Write([]byte(fmt.Sprintf("%016d", metricLabels.Hash())))
	h.Write([]byte(metricName))
	h.Write([]byte(fmt.Sprintf("%016d", metricType)))
	return ResetPointKey(h.Sum64())
}

func (t *Target) GetResetPoint(key ResetPointKey) (point *Point) {
	t.mtx.RLock()
	defer t.mtx.RUnlock()

	if point, ok := t.resetPoints[key]; ok {
		return &point
	}
	return nil
}

func (t *Target) AddResetPoint(key ResetPointKey, point Point) {
	t.mtx.Lock()
	defer t.mtx.Unlock()

	t.resetPoints[key] = point
}

func (t *Target) HasResetPoints() bool {
	t.mtx.RLock()
	defer t.mtx.RUnlock()

	return len(t.resetPoints) > 0
}
