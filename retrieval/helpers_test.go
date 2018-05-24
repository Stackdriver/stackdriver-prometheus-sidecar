// Copyright 2013 The Prometheus Authors
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
)

type nopAppendable struct{}

func (a nopAppendable) Appender() (Appender, error) {
	return nopAppender{}, nil
}

type nopAppender struct{}

func (a nopAppender) Add(metricFamily *MetricFamily) error { return nil }

// collectResultAppender records all samples that were added through the appender.
// It can be used as its zero value or be backed by another appender it writes samples through.
type collectResultAppender struct {
	next   Appender
	result []*MetricFamily
}

func (a *collectResultAppender) Add(metricFamily *MetricFamily) error {
	a.result = append(a.result, metricFamily)
	if a.next == nil {
		return nil
	}
	return a.next.Add(metricFamily)
}

// ByName implements sort.Interface for []*MetricFamily based on the Name
// field.
type ByName []*MetricFamily

func (f ByName) Len() int           { return len(f) }
func (f ByName) Swap(i, j int)      { f[i], f[j] = f[j], f[i] }
func (f ByName) Less(i, j int) bool { return f[i].GetName() < f[j].GetName() }

// Sorted returns the collected samples in deterministic order, to help in tests.
func (a *collectResultAppender) Sorted() []*MetricFamily {
	tmp := make([]*MetricFamily, len(a.result))
	copy(tmp, a.result)
	sort.Sort(ByName(tmp))
	return tmp
}

func (a *collectResultAppender) Reset() {
	a.result = nil
}

func mustMetricFamily(metricFamily *MetricFamily, err error) *MetricFamily {
	if err != nil {
		panic(err)
	}
	return metricFamily
}
