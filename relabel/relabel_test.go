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
package relabel

import (
	"fmt"
	"testing"

	"github.com/gogo/protobuf/proto"
	"github.com/prometheus/common/model"
	"github.com/prometheus/prometheus/config"
	"github.com/prometheus/tsdb/labels"
)

func BenchmarkRelabel(b *testing.B) {
	b.ReportAllocs()
	targetLabels := map[string]*labels.Label{}
	for i := 0; i < 10; i++ {
		key := fmt.Sprintf("target_label%d", i)
		value := fmt.Sprintf("%d:foo oaeu aoeu aoeu aoeu ou", i)
		targetLabels[key] = &labels.Label{Name: key, Value: value}
	}
	metricLabels := LabelPairs{
		{
			Name:  proto.String("metric_label1"),
			Value: proto.String("metric_value1"),
		},
		{
			Name:  proto.String("metric_label2"),
			Value: proto.String("metric_value2"),
		},
	}
	configs := []*config.RelabelConfig{
		{
			Action:       config.RelabelReplace,
			Regex:        config.MustNewRegexp("(.*)"),
			SourceLabels: model.LabelNames{"my_key"},
			Replacement:  "${1}_copy",
			TargetLabel:  "my_key_copy",
		},
		{
			Action:      config.RelabelLabelMap,
			Regex:       config.MustNewRegexp("(.*)"),
			Replacement: "${1}_clone",
		},
	}

	for i := 0; i < b.N; i++ {
		Process(metricLabels, configs...)
	}
}
