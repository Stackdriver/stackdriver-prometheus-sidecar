package opencensus

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"go.opencensus.io/metric/metricdata"
	"go.opencensus.io/metric/metricexport"
	"go.opencensus.io/stats/view"
)

// TestExporter keeps exported metric data in memory to aid in testing the instrumentation.
//
// Metrics can be retrieved with `GetPoint()`. In order to deterministically retrieve the most recent values, you must first invoke `ReadAndExport()`.
type TestExporter struct {
	// points is a map from a label signature to the latest value for the time series represented by the signature.
	// Use function `labelSignature` to get a signature from a `metricdata.Metric`.
	points       map[string]metricdata.Point
	metricReader *metricexport.Reader
}

// NewTestExporter returns a new exporter.
func NewTestExporter(metricReader *metricexport.Reader) *TestExporter {
	return &TestExporter{points: make(map[string]metricdata.Point), metricReader: metricReader}
}

// ExportMetrics records the view data.
func (e *TestExporter) ExportMetrics(ctx context.Context, data []*metricdata.Metric) error {
	for _, metric := range data {
		for _, ts := range metric.TimeSeries {
			signature := labelSignature(metric.Descriptor.Name, labelObjectsToKeyValue(metric.Descriptor.LabelKeys, ts.LabelValues))
			e.points[signature] = ts.Points[len(ts.Points)-1]
		}
	}
	return nil
}

// GetPoint returns the latest point for the time series identified by the given labels.
func (e *TestExporter) GetPoint(metricName string, labels map[string]string) (metricdata.Point, bool) {
	v, ok := e.points[labelSignature(metricName, labelMapToKeyValue(labels))]
	return v, ok
}

// ReadAndExport reads the current values for all metrics and makes them available to this exporter.
func (e *TestExporter) ReadAndExport() {
	// The next line forces the view worker to process all stats.Record* calls that
	// happened within Store() before the call to ReadAndExport below. This abuses the
	// worker implementation to work around lack of synchronization.
	// TODO(jkohen,rghetia): figure out a clean way to make this deterministic.
	view.SetReportingPeriod(time.Minute)
	e.metricReader.ReadAndExport(e)
}

func (e *TestExporter) String() string {
	return fmt.Sprintf("points{%v}", e.points)
}

type keyValue struct {
	Key   string
	Value string
}

func sortKeyValue(kv []keyValue) {
	sort.Slice(kv, func(i, j int) bool { return kv[i].Key < kv[j].Key })
}

func labelMapToKeyValue(labels map[string]string) []keyValue {
	kv := make([]keyValue, 0, len(labels))
	for k, v := range labels {
		kv = append(kv, keyValue{Key: k, Value: v})
	}
	sortKeyValue(kv)
	return kv
}

func labelObjectsToKeyValue(keys []metricdata.LabelKey, values []metricdata.LabelValue) []keyValue {
	kv := make([]keyValue, 0, len(values))
	for i := range keys {
		if values[i].Present {
			kv = append(kv, keyValue{Key: keys[i].Key, Value: values[i].Value})
		}
	}
	sortKeyValue(kv)
	return kv
}

// labelSignature returns a string that uniquely identifies the list of labels given in the input.
func labelSignature(metricName string, kv []keyValue) string {
	var builder strings.Builder
	for _, x := range kv {
		builder.WriteString(x.Key)
		builder.WriteString(x.Value)
	}
	return fmt.Sprintf("%s{%s}", metricName, builder.String())
}
