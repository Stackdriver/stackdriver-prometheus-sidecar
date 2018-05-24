/*
Copyright 2017 Google Inc.
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
package stackdriver

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/Stackdriver/stackdriver-prometheus-sidecar/retrieval"
	"github.com/go-kit/kit/log"
	"github.com/go-kit/kit/log/level"
	"github.com/golang/glog"
	timestamp_pb "github.com/golang/protobuf/ptypes/timestamp"
	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/prometheus/pkg/labels"
	"github.com/prometheus/prometheus/pkg/timestamp"
	distribution_pb "google.golang.org/genproto/googleapis/api/distribution"
	metric_pb "google.golang.org/genproto/googleapis/api/metric"
	monitoredres_pb "google.golang.org/genproto/googleapis/api/monitoredres"
	monitoring_pb "google.golang.org/genproto/googleapis/monitoring/v3"
)

var supportedMetricTypes = map[dto.MetricType]struct{}{
	dto.MetricType_COUNTER:   struct{}{},
	dto.MetricType_GAUGE:     struct{}{},
	dto.MetricType_HISTOGRAM: struct{}{},
	dto.MetricType_SUMMARY:   struct{}{},
	dto.MetricType_UNTYPED:   struct{}{},
}

const (
	falseValueEpsilon = 0.001
	maxLabelCount     = 10
)

type unsupportedTypeError struct {
	metricType dto.MetricType
}

func (e *unsupportedTypeError) Error() string {
	return e.metricType.String()
}

// Translator allows converting Prometheus samples to Stackdriver TimeSeries.
type Translator struct {
	logger           log.Logger
	metricsPrefix    string
	resourceMappings []ResourceMap
}

// NewTranslator creates a new Translator.
func NewTranslator(logger log.Logger, metricsPrefix string, resourceMappings []ResourceMap) *Translator {
	return &Translator{
		logger:           logger,
		metricsPrefix:    metricsPrefix,
		resourceMappings: resourceMappings,
	}
}

// ToCreateTimeSeriesRequest translates metrics in Prometheus format to Stackdriver format.
func (t *Translator) ToCreateTimeSeriesRequest(
	metrics []*retrieval.MetricFamily) *monitoring_pb.CreateTimeSeriesRequest {

	// TODO(jkohen): See if it's possible for Prometheus to pass two points
	// for the same time series, which isn't accepted by the Stackdriver
	// Monitoring API.
	request := &monitoring_pb.CreateTimeSeriesRequest{}
	for _, family := range metrics {
		tss, err := t.translateFamily(family)
		if err != nil {
			// Ignore unsupported type errors, they're just noise.
			if _, ok := err.(*unsupportedTypeError); !ok {
				level.Warn(t.logger).Log(
					"msg", "error while processing metric",
					"metric", family.GetName(),
					"err", err)
			}
		} else {
			request.TimeSeries = append(request.TimeSeries, tss...)
		}
	}
	return request
}

func (t *Translator) translateFamily(family *retrieval.MetricFamily) ([]*monitoring_pb.TimeSeries, error) {
	if _, found := supportedMetricTypes[family.GetType()]; !found {
		return nil, &unsupportedTypeError{family.GetType()}
	}
	// This isn't exact, because not all metric types map to a single time
	// series. Notoriously, summary maps to 2 or more.
	tss := make([]*monitoring_pb.TimeSeries, 0, len(family.GetMetric()))
	for i, metric := range family.GetMetric() {
		startTime := timestamp.Time(family.MetricResetTimestampMs[i])
		monitoredResource := t.getMonitoredResource(family.TargetLabels, metric.GetLabel())
		if monitoredResource == nil {
			// Metrics are usually independent, so just drop this one.
			level.Warn(t.logger).Log(
				"msg", "cannot extract Stackdriver monitored resource from metric",
				"family", family.GetName(),
				"target_labels", family.TargetLabels,
				"metric", metric)
			continue
		}
		switch family.GetType() {
		case dto.MetricType_SUMMARY:
			ts, err := t.translateSummary(family.GetName(), monitoredResource, metric, startTime)
			if err != nil {
				// Metrics are usually independent, so just drop this one.
				level.Warn(t.logger).Log(
					"msg", "error while processing metric",
					"family", family.GetName(),
					"metric", metric,
					"err", err)
				continue
			}
			tss = append(tss, ts...)
		default:
			ts, err := t.translateOne(family.GetName(), monitoredResource, family.GetType(), metric, startTime)
			if err != nil {
				// Metrics are usually independent, so just drop this one.
				level.Warn(t.logger).Log(
					"msg", "error while processing metric",
					"family", family.GetName(),
					"metric", metric,
					"err", err)
				continue
			}
			tss = append(tss, ts)
		}
	}
	return tss, nil
}

// getMetricType creates metric type name base on the metric prefix, and metric name.
func getMetricType(metricsPrefix string, name string) string {
	// This does no allocations, versus 12 with fmt.Sprintf.
	return metricsPrefix + "/" + name
}

func getTimestamp(ts time.Time) *timestamp_pb.Timestamp {
	return &timestamp_pb.Timestamp{
		Seconds: ts.Truncate(time.Second).Unix(),
		Nanos:   int32(ts.Nanosecond()),
	}
}

// assumes that mType is Counter, Gauge, Untyped, or Histogram. Returns nil on error.
func (t *Translator) translateOne(name string,
	monitoredResource *monitoredres_pb.MonitoredResource,
	mType dto.MetricType,
	metric *dto.Metric,
	start time.Time) (*monitoring_pb.TimeSeries, error) {
	interval := &monitoring_pb.TimeInterval{
		EndTime: getTimestamp(timestamp.Time(metric.GetTimestampMs()).UTC()),
	}
	metricKind := extractMetricKind(mType)
	if metricKind == metric_pb.MetricDescriptor_CUMULATIVE {
		interval.StartTime = getTimestamp(start.UTC())
	}
	valueType := extractValueType(mType)
	point := &monitoring_pb.Point{
		Interval: interval,
	}
	setValue(mType, valueType, metric, point)

	tsLabels := getMetricLabels(metric.GetLabel())
	if len(tsLabels) > maxLabelCount {
		return nil, fmt.Errorf(
			"dropping metric because it has more than %v labels, and Stackdriver would reject it",
			maxLabelCount)
	}
	return &monitoring_pb.TimeSeries{
		Metric: &metric_pb.Metric{
			Labels: tsLabels,
			Type:   getMetricType(t.metricsPrefix, name),
		},
		Resource:   monitoredResource,
		MetricKind: metricKind,
		ValueType:  valueType,
		Points:     []*monitoring_pb.Point{point},
	}, nil
}

// assumes that mType is Counter, Gauge, Untyped, or Histogram. Returns nil on error.
func (t *Translator) translateSummary(name string,
	monitoredResource *monitoredres_pb.MonitoredResource,
	metric *dto.Metric,
	start time.Time) ([]*monitoring_pb.TimeSeries, error) {
	interval := &monitoring_pb.TimeInterval{
		EndTime: getTimestamp(timestamp.Time(metric.GetTimestampMs()).UTC()),
	}
	tsLabels := getMetricLabels(metric.GetLabel())
	if len(tsLabels) > maxLabelCount {
		return nil, fmt.Errorf(
			"dropping metric because it has more than %v labels, and Stackdriver would reject it",
			maxLabelCount)
	}

	baseMetricType := getMetricType(t.metricsPrefix, name)
	summary := metric.GetSummary()
	tss := make([]*monitoring_pb.TimeSeries, 2+len(summary.GetQuantile()))
	// Sum metric. Summary works over a sliding window, so this value could go down, hence GAUGE.
	tss[0] = &monitoring_pb.TimeSeries{
		Metric: &metric_pb.Metric{
			Labels: tsLabels,
			Type:   baseMetricType + "_sum",
		},
		Resource:   monitoredResource,
		MetricKind: metric_pb.MetricDescriptor_GAUGE,
		ValueType:  metric_pb.MetricDescriptor_DOUBLE,
		Points: []*monitoring_pb.Point{
			{
				Interval: interval,
				Value: &monitoring_pb.TypedValue{
					&monitoring_pb.TypedValue_DoubleValue{DoubleValue: summary.GetSampleSum()},
				},
			},
		},
	}
	// Count metric. Summary works over a sliding window, so this value could go down, hence GAUGE.
	tss[1] = &monitoring_pb.TimeSeries{
		Metric: &metric_pb.Metric{
			Labels: tsLabels,
			Type:   baseMetricType + "_count",
		},
		Resource:   monitoredResource,
		MetricKind: metric_pb.MetricDescriptor_CUMULATIVE,
		ValueType:  metric_pb.MetricDescriptor_INT64,
		Points: []*monitoring_pb.Point{
			{
				Interval: &monitoring_pb.TimeInterval{
					StartTime: getTimestamp(start.UTC()),
					EndTime:   getTimestamp(timestamp.Time(metric.GetTimestampMs()).UTC()),
				},
				Value: &monitoring_pb.TypedValue{
					&monitoring_pb.TypedValue_Int64Value{Int64Value: int64(summary.GetSampleCount())},
				},
			},
		},
	}

	for i, quantile := range summary.GetQuantile() {
		qLabels := make(map[string]string, len(tsLabels))
		for k, v := range tsLabels {
			qLabels[k] = v
		}
		// Format using ddd.dddd format (no exponent) with the minimum number of digits necessary.
		qLabels["quantile"] = strconv.FormatFloat(quantile.GetQuantile(), 'f', -1, 64)
		tss[i+2] = &monitoring_pb.TimeSeries{
			Metric: &metric_pb.Metric{
				Labels: qLabels,
				Type:   baseMetricType,
			},
			Resource:   monitoredResource,
			MetricKind: metric_pb.MetricDescriptor_GAUGE,
			ValueType:  metric_pb.MetricDescriptor_DOUBLE,
			Points: []*monitoring_pb.Point{
				{
					Interval: interval,
					Value: &monitoring_pb.TypedValue{
						&monitoring_pb.TypedValue_DoubleValue{DoubleValue: quantile.GetValue()},
					},
				},
			},
		}
	}

	return tss, nil
}

func setValue(
	mType dto.MetricType, valueType metric_pb.MetricDescriptor_ValueType,
	metric *dto.Metric, point *monitoring_pb.Point) {

	point.Value = &monitoring_pb.TypedValue{}
	switch mType {
	case dto.MetricType_GAUGE:
		setValueBaseOnSimpleType(metric.GetGauge().GetValue(), valueType, point)
	case dto.MetricType_COUNTER:
		setValueBaseOnSimpleType(metric.GetCounter().GetValue(), valueType, point)
	case dto.MetricType_HISTOGRAM:
		point.Value = &monitoring_pb.TypedValue{
			Value: &monitoring_pb.TypedValue_DistributionValue{
				DistributionValue: convertToDistributionValue(metric.GetHistogram()),
			},
		}
	case dto.MetricType_UNTYPED:
		setValueBaseOnSimpleType(metric.GetUntyped().GetValue(), valueType, point)
	}
}

func setValueBaseOnSimpleType(
	value float64, valueType metric_pb.MetricDescriptor_ValueType,
	point *monitoring_pb.Point) {

	if valueType == metric_pb.MetricDescriptor_DOUBLE {
		point.Value = &monitoring_pb.TypedValue{
			Value: &monitoring_pb.TypedValue_DoubleValue{DoubleValue: value},
		}
	} else {
		glog.Errorf("Value type '%s' is not supported yet.", valueType)
	}
}

func convertToDistributionValue(h *dto.Histogram) *distribution_pb.Distribution {
	count := int64(h.GetSampleCount())
	mean := float64(0)
	dev := float64(0)
	bounds := make([]float64, 0, len(h.Bucket))
	values := make([]int64, 0, len(h.Bucket))

	if count > 0 {
		mean = h.GetSampleSum() / float64(count)
	}

	prevVal := uint64(0)
	lower := float64(0)
	for _, b := range h.Bucket {
		upper := b.GetUpperBound()
		if math.IsInf(upper, 1) {
			upper = lower
		} else {
			bounds = append(bounds, upper)
		}
		val := b.GetCumulativeCount() - prevVal
		x := (lower + upper) / float64(2)
		dev += float64(val) * (x - mean) * (x - mean)

		values = append(values, int64(b.GetCumulativeCount()-prevVal))

		lower = b.GetUpperBound()
		prevVal = b.GetCumulativeCount()
	}

	return &distribution_pb.Distribution{
		Count: count,
		Mean:  mean,
		SumOfSquaredDeviation: dev,
		BucketOptions: &distribution_pb.Distribution_BucketOptions{
			Options: &distribution_pb.Distribution_BucketOptions_ExplicitBuckets{
				ExplicitBuckets: &distribution_pb.Distribution_BucketOptions_Explicit{
					Bounds: bounds,
				},
			},
		},
		BucketCounts: values,
	}
}

// getMetricLabels returns a Stackdriver label map from the label.
//
// By convention it excludes any Prometheus labels with "_" prefix, which
// includes the labels that correspond to Stackdriver resource labels.
func getMetricLabels(labels []*dto.LabelPair) map[string]string {
	metricLabels := map[string]string{}
	for _, label := range labels {
		if strings.HasPrefix(label.GetName(), "_") {
			continue
		}
		metricLabels[label.GetName()] = label.GetValue()
	}
	return metricLabels
}

func extractMetricKind(mType dto.MetricType) metric_pb.MetricDescriptor_MetricKind {
	if mType == dto.MetricType_COUNTER || mType == dto.MetricType_HISTOGRAM {
		return metric_pb.MetricDescriptor_CUMULATIVE
	}
	return metric_pb.MetricDescriptor_GAUGE
}

func extractValueType(mType dto.MetricType) metric_pb.MetricDescriptor_ValueType {
	if mType == dto.MetricType_HISTOGRAM {
		return metric_pb.MetricDescriptor_DISTRIBUTION
	}
	return metric_pb.MetricDescriptor_DOUBLE
}

func (t *Translator) getMonitoredResource(
	targetLabels labels.Labels, metricLabels []*dto.LabelPair) *monitoredres_pb.MonitoredResource {
	for _, resource := range t.resourceMappings {
		if labels := resource.Translate(targetLabels, metricLabels); labels != nil {
			return &monitoredres_pb.MonitoredResource{
				Type:   resource.Type,
				Labels: labels,
			}
		}
	}
	return nil
}
