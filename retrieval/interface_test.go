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

	"github.com/gogo/protobuf/proto"
	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/model"
	"github.com/prometheus/prometheus/pkg/labels"
)

const instanceLabel = model.InstanceLabel

func metricFamilyFromLabelPairs(targetLabels labels.Labels, pairs ...string) *MetricFamily {
	metric := &dto.Metric{}
	for i := 0; i < len(pairs); i += 2 {
		metric.Label = append(metric.Label,
			&dto.LabelPair{
				Name:  proto.String(pairs[i]),
				Value: proto.String(pairs[i+1]),
			})
	}
	metricFamily, err := NewMetricFamily(
		&dto.MetricFamily{
			Name:   proto.String("mysql_info_schema_table_rows"),
			Metric: []*dto.Metric{metric},
		},
		[]int64{12334124234},
		targetLabels)
	if err != nil {
		panic(err)
	}
	return metricFamily
}

func TestMetricFamilyFingerprint(t *testing.T) {
	family1 := metricFamilyFromLabelPairs(labels.Labels{{"rk1", "rv1"}}, "l1", "v1", "l2", "v2", instanceLabel, "i1")
	family2 := metricFamilyFromLabelPairs(labels.Labels{{"rk1", "rv1"}}, "l2", "v2", "l1", "v1", instanceLabel, "i1")
	// Must be equal regardless of label order.
	if family1.Fingerprint() != family2.Fingerprint() {
		t.Errorf("expected equal fingerprints, got %x and %x", family1.Fingerprint(), family2.Fingerprint())
	}
	family3 := metricFamilyFromLabelPairs(labels.Labels{{"rk1", "rv1"}}, "l1", "v1", "l2", "v3", instanceLabel, "i1")
	// Must be different if labels are different.
	if family1.Fingerprint() == family3.Fingerprint() {
		t.Errorf("expected different fingerprints, got %x and %x", family1.Fingerprint(), family3.Fingerprint())
	}
	family4 := metricFamilyFromLabelPairs(labels.Labels{{"rk1", "rv2"}}, "l1", "v1", "l2", "v3", instanceLabel, "i1")
	// Must be equal if only target labels differ. In practise the instance label would be different, too, but check the corner case.
	if family3.Fingerprint() != family4.Fingerprint() {
		t.Errorf("expected equal fingerprints, got %x and %x", family3.Fingerprint(), family4.Fingerprint())
	}

	// Must be different if instanceLabel is different, after it's been removed from the metric labels. In practise users can do this with relabelling.
	family5 := metricFamilyFromLabelPairs(labels.Labels{{"rk1", "rv2"}}, "l1", "v1", instanceLabel, "i1")
	family6 := metricFamilyFromLabelPairs(labels.Labels{{"rk1", "rv2"}}, "l1", "v1", instanceLabel, "i2")
	if family5.Fingerprint() == family6.Fingerprint() {
		t.Errorf("expected different fingerprints, got %x and %x", family5.Fingerprint(), family6.Fingerprint())
	}
	family5.Metric[0].Label = family5.Metric[0].Label[0:0]
	family6.Metric[0].Label = family5.Metric[0].Label[0:0]
	if family5.Fingerprint() == family6.Fingerprint() {
		t.Errorf("expected different fingerprints, got %x and %x", family5.Fingerprint(), family6.Fingerprint())
	}
}

func BenchmarkMetricFamilyFingerprint(b *testing.B) {
	b.ReportAllocs()
	metricFamily := &MetricFamily{
		MetricFamily: &dto.MetricFamily{
			Name: proto.String("mysql_info_schema_table_rows"),
			Help: proto.String("The estimated number of rows in the table from information_schema.tables"),
			Type: dto.MetricType_GAUGE.Enum(),
			Metric: []*dto.Metric{
				&dto.Metric{
					Label: []*dto.LabelPair{
						{Name: proto.String("schema"), Value: proto.String("wordpress")},
						{Name: proto.String("table"), Value: proto.String("wp_commentmeta")},
					},
				},
			},
		},
		MetricResetTimestampMs: []int64{12334124234},
	}
	for i := 0; i < b.N; i++ {
		metricFamily.Fingerprint()
	}
}
