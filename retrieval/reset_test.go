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
	"testing"
	"time"

	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/prometheus/pkg/labels"
)

func TestResetPointKey(t *testing.T) {
	lbls := labels.FromMap(map[string]string{
		"x": "123",
		"y": "456",
	})
	k1 := NewResetPointKey("foo", lbls, dto.MetricType_COUNTER)
	k1b := NewResetPointKey("foo", lbls, dto.MetricType_COUNTER)
	if k1 != k1b {
		t.Fatalf("expected %v to be equal to %v", k1, k1b)
	}

	k2 := NewResetPointKey("foo2", lbls, dto.MetricType_COUNTER)
	if k1 == k2 {
		t.Fatalf("expected %v to be different from %v", k1, k2)
	}

	lbls3 := labels.FromMap(map[string]string{
		"x": "123",
	})
	k3 := NewResetPointKey("foo", lbls3, dto.MetricType_COUNTER)
	if k1 == k3 {
		t.Fatalf("expected %v to be different from %v", k1, k3)
	}

	k4 := NewResetPointKey("foo", lbls, dto.MetricType_HISTOGRAM)
	if k1 == k4 {
		t.Fatalf("expected %v to be different from %v", k1, k4)
	}

	k5 := NewResetPointKey("foo", lbls, dto.MetricType_SUMMARY)
	if k1 == k5 {
		t.Fatalf("expected %v to be different from %v", k1, k5)
	}
}

func TestTargetResetPoint(t *testing.T) {
	lbls := labels.FromStrings("x", "123")
	target := newTestTarget("example.com:80", 0, labels.FromStrings(
		"label", "0",
	))
	if target.HasResetPoints() {
		t.Fatal("expected HasResetPoints() to be false")
	}
	key := NewResetPointKey("foo", lbls, dto.MetricType_COUNTER)
	{
		result := target.GetResetPoint(key)
		if result != nil {
			t.Fatalf("expected nil, got %v", result)
		}
	}
	timestamp := time.Now()
	point := Point{Timestamp: timestamp, ResetValue: &PointValue{Counter: 123}}
	target.AddResetPoint(key, point)
	{
		result := target.GetResetPoint(key)
		if result == nil {
			t.Fatalf("expected %v, got nil", point)
		}
		if point != *result {
			t.Fatalf("expected %v, got %v", point, result)
		}
		if !target.HasResetPoints() {
			t.Fatal("expected HasResetPoints() to be true")
		}
	}
	{
		key2 := NewResetPointKey("bar", lbls, dto.MetricType_COUNTER)
		result := target.GetResetPoint(key2)
		if result != nil {
			t.Fatalf("expected nil, got %v", result)
		}
	}
}
