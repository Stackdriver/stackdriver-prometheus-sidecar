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

package stackdriver

import (
	"fmt"
	"hash/fnv"
	"reflect"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Stackdriver/stackdriver-prometheus-sidecar/retrieval"
	"github.com/gogo/protobuf/proto"
	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/model"
	"github.com/prometheus/prometheus/config"
	"github.com/prometheus/prometheus/pkg/labels"
	metric_pb "google.golang.org/genproto/googleapis/api/metric"
	monitoring "google.golang.org/genproto/googleapis/monitoring/v3"
)

type TestStorageClient struct {
	receivedSamples map[string][]sample
	expectedSamples map[string][]sample
	wg              sync.WaitGroup
	mtx             sync.Mutex
	t               *testing.T
}

type sample struct {
	Name           string
	Labels         map[string]string
	Value          float64
	ResetTimestamp int64
	Timestamp      int64
}

var (
	TestTargetLabel = labels.Label{Name: "__target_label", Value: "1234"}
)

func init() {
	// Override the default resource mappings, so they only require the
	// project id resource label, which makes the tests more concise.
	DefaultResourceMappings = []ResourceMap{
		{
			Type: "global",
			LabelMap: map[string]string{
				ProjectIdLabel:       "project_id",
				TestTargetLabel.Name: TestTargetLabel.Value,
			},
		},
	}
}

func NewTestStorageClient(t *testing.T) *TestStorageClient {
	return &TestStorageClient{
		receivedSamples: map[string][]sample{},
		expectedSamples: map[string][]sample{},
		t:               t,
	}
}

func (c *TestStorageClient) expectSamples(samples []sample) {
	c.mtx.Lock()
	defer c.mtx.Unlock()
	for _, s := range samples {
		c.expectedSamples[s.Name] = append(c.expectedSamples[s.Name], s)
	}
	c.wg.Add(len(samples))
}

func (c *TestStorageClient) waitForExpectedSamples(t *testing.T) {
	c.wg.Wait()

	c.mtx.Lock()
	defer c.mtx.Unlock()
	if len(c.receivedSamples) != len(c.expectedSamples) {
		t.Fatalf("Expected %d metric families, received %d",
			len(c.expectedSamples), len(c.receivedSamples))
	}
	for name, expectedSamples := range c.expectedSamples {
		if !reflect.DeepEqual(expectedSamples, c.receivedSamples[name]) {
			t.Fatalf("%s: Expected %v, got %v", name, expectedSamples, c.receivedSamples[name])
		}
	}
}

func (c *TestStorageClient) resetExpectedSamples() {
	c.receivedSamples = map[string][]sample{}
	c.expectedSamples = map[string][]sample{}
}

func fingerprintMetric(metric *metric_pb.Metric) uint64 {
	const SeparatorByte byte = 255
	h := fnv.New64a()
	h.Write([]byte(metric.GetType()))
	// Sort the labels to get a deterministic fingerprint.
	var keys []string
	for k := range metric.GetLabels() {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		v := metric.GetLabels()[k]
		h.Write([]byte{SeparatorByte})
		h.Write([]byte(k))
		h.Write([]byte{SeparatorByte})
		h.Write([]byte(v))
	}
	return h.Sum64()
}

func (c *TestStorageClient) Store(req *monitoring.CreateTimeSeriesRequest) error {
	c.mtx.Lock()
	defer c.mtx.Unlock()
	seenTimeSeries := map[uint64]struct{}{}
	count := 0
	for _, ts := range req.TimeSeries {
		fp := fingerprintMetric(ts.Metric)
		if _, ok := seenTimeSeries[fp]; ok {
			c.t.Errorf("found duplicate time series in request: %v", ts)
		}
		seenTimeSeries[fp] = struct{}{}
		metricType := ts.Metric.Type
		// Remove the Stackdriver "domain/" prefix which isn't present in the test input.
		name := metricType[len(metricsPrefix)+1:]
		for _, point := range ts.Points {
			count++
			startTime := point.GetInterval().GetStartTime()
			var resetTimeMs int64
			if startTime != nil {
				resetTimeMs = time.Unix(startTime.Seconds, int64(startTime.Nanos)).UnixNano() / 1000000
			}
			endTime := point.GetInterval().GetEndTime()
			endTimeMs := time.Unix(endTime.Seconds, int64(endTime.Nanos)).UnixNano() / 1000000
			s := sample{
				Name:           name,
				Labels:         ts.Metric.Labels,
				Value:          point.Value.GetDoubleValue(),
				ResetTimestamp: resetTimeMs,
				Timestamp:      endTimeMs,
			}
			c.receivedSamples[name] = append(c.receivedSamples[name], s)
		}
	}
	c.wg.Add(-count)
	return nil
}

func (t *TestStorageClient) New() StorageClient {
	return t
}

func (c *TestStorageClient) Name() string {
	return "teststorageclient"
}

func (c *TestStorageClient) Close() error {
	c.wg.Wait()
	return nil
}

func TestSampleDelivery(t *testing.T) {
	// Let's create an even number of send batches so we don't run into the
	// batch timeout case.
	n := 100

	samples := make([]sample, 0, n)
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("test_metric_%d", i)
		samples = append(samples, sample{
			Name: name,
			Labels: map[string]string{
				model.InstanceLabel: strconv.Itoa(i),
			},
			Value:          float64(i),
			ResetTimestamp: 1234567890000,
			Timestamp:      2234567890000,
		})
	}

	c := NewTestStorageClient(t)
	c.expectSamples(samples)

	cfg := config.DefaultQueueConfig
	cfg.Capacity = n
	cfg.MaxSamplesPerSend = n
	m := NewQueueManager(nil, cfg, c)

	// These should be received by the client.
	for _, s := range samples {
		m.Append(samplesToMetricFamily(s))
	}
	m.Start()
	defer m.Stop()

	c.waitForExpectedSamples(t)
}

func TestSampleDeliveryMultiShard(t *testing.T) {
	numShards := 10
	n := 5 * numShards

	samples := make([]sample, 0, n)
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("test_metric_%d", i)
		samples = append(samples, sample{
			Name: name,
			Labels: map[string]string{
				model.InstanceLabel: strconv.Itoa(i),
			},
			Value:          float64(i),
			ResetTimestamp: 1234567890000,
			Timestamp:      2234567890000,
		})
	}

	c := NewTestStorageClient(t)

	cfg := config.DefaultQueueConfig
	// flush after each sample, to avoid blocking the test
	cfg.MaxSamplesPerSend = 1
	cfg.MaxShards = numShards
	m := NewQueueManager(nil, cfg, c)
	m.Start()
	defer m.Stop()
	m.reshard(numShards) // blocks until resharded

	c.expectSamples(samples)
	// These should be received by the client.
	for _, s := range samples {
		m.Append(samplesToMetricFamily(s))
	}

	c.waitForExpectedSamples(t)
}

func TestSampleDeliveryTimeout(t *testing.T) {
	// Let's send one less sample than batch size, and wait the timeout duration
	n := config.DefaultQueueConfig.MaxSamplesPerSend - 1

	samples := make([]sample, 0, n)
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("test_metric_%d", i)
		samples = append(samples, sample{
			Name: name,
			Labels: map[string]string{
				model.InstanceLabel: strconv.Itoa(i),
			},
			Value:          float64(i),
			ResetTimestamp: 1234567890000,
			Timestamp:      2234567890000,
		})
	}

	c := NewTestStorageClient(t)
	cfg := config.DefaultQueueConfig
	cfg.MaxShards = 1
	cfg.BatchSendDeadline = 100 * time.Millisecond
	m := NewQueueManager(nil, cfg, c)
	m.Start()
	defer m.Stop()

	// Send the samples twice, waiting for the samples in the meantime.
	c.expectSamples(samples)
	for _, s := range samples {
		m.Append(samplesToMetricFamily(s))
	}
	c.waitForExpectedSamples(t)

	c.resetExpectedSamples()
	for i := range samples {
		samples[i].Timestamp += 1
	}
	c.expectSamples(samples)
	for _, s := range samples {
		m.Append(samplesToMetricFamily(s))
	}
	c.waitForExpectedSamples(t)
}

func TestSampleDeliveryOrder(t *testing.T) {
	ts := 10
	n := config.DefaultQueueConfig.MaxSamplesPerSend * ts

	samples := make([]sample, 0, n)
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("test_metric_%d", i%ts)
		samples = append(samples, sample{
			Name: name,
			Labels: map[string]string{
				model.InstanceLabel: strconv.Itoa(i),
			},
			Value:          float64(i),
			ResetTimestamp: 1234567890001,
			Timestamp:      1234567890001 + int64(i),
		})
	}

	c := NewTestStorageClient(t)
	c.expectSamples(samples)
	m := NewQueueManager(nil, config.DefaultQueueConfig, c)

	m.Start()
	defer m.Stop()
	// These should be received by the client.
	for _, s := range samples {
		m.Append(samplesToMetricFamily(s))
	}

	c.waitForExpectedSamples(t)
}

func TestSampleOutOfOrder(t *testing.T) {
	n := config.DefaultQueueConfig.MaxSamplesPerSend

	samples := make([]sample, 0, n)
	for i := 0; i < n; i++ {
		samples = append(samples, sample{
			Name: "test_metric",
			Labels: map[string]string{
				"key":               fmt.Sprintf("%d", i),
				model.InstanceLabel: strconv.Itoa(i),
			},
			Value:          float64(i),
			ResetTimestamp: 1234567890001,
			Timestamp:      2234567890001,
		})
	}

	c := NewTestStorageClient(t)
	c.expectSamples(samples)
	m := NewQueueManager(nil, config.DefaultQueueConfig, c)

	m.Start()
	defer m.Stop()
	for _, s := range samples {
		// These should be received by the client.
		m.Append(samplesToMetricFamily(s))
	}
	for _, s := range samples {
		// Same reset and value timestamp should be dropped.
		m.Append(samplesToMetricFamily(s))
	}
	for _, s := range samples {
		// Same reset timestamp and older value timestamp should be dropped.
		s.Timestamp -= 1
		m.Append(samplesToMetricFamily(s))
	}
	for _, s := range samples {
		// Older reset timestamp should be dropped regardless of value timestamp.
		s.ResetTimestamp -= 1
		s.Timestamp += 1000
		m.Append(samplesToMetricFamily(s))
	}

	c.waitForExpectedSamples(t)
}

// TestSampleOutOfOrderMultiShard is a specialized version of
// TestSampleOutOfOrder that checks that out-of-order samples can be detected
// after resharding. TestSampleOutOfOrder does a more exhaustive test of the
// logic that detects out-of-order samples in general.
func TestSampleOutOfOrderMultiShard(t *testing.T) {
	numShards := 10
	n := 100 * numShards

	samples := make([]sample, 0, n)
	for i := 0; i < n; i++ {
		samples = append(samples, sample{
			Name: "test_metric",
			Labels: map[string]string{
				"key":               fmt.Sprintf("%d", i),
				model.InstanceLabel: strconv.Itoa(i),
			},
			Value:          float64(i),
			ResetTimestamp: 1234567890001,
			Timestamp:      2234567890001,
		})
	}

	c := NewTestStorageClient(t)

	cfg := config.DefaultQueueConfig
	// flush after each sample, to avoid blocking the test
	cfg.MaxSamplesPerSend = 1
	cfg.MaxShards = numShards
	m := NewQueueManager(nil, config.DefaultQueueConfig, c)
	m.Start()
	defer m.Stop()

	c.expectSamples(samples)
	for _, s := range samples {
		// These should be received by the client.
		m.Append(samplesToMetricFamily(s))
	}
	c.waitForExpectedSamples(t)

	c.resetExpectedSamples()
	m.reshard(numShards)        // blocks until resharded
	c.expectSamples([]sample{}) // all samples should be dropped
	for _, s := range samples {
		m.Append(samplesToMetricFamily(s))
	}
	c.waitForExpectedSamples(t)
}

// TestEmptyRequest tests the case where the output of the translation is empty
// (e.g. because all metric types are unsupported), and therefore there is
// nothing to send to the storage.
func TestStoreEmptyRequest(t *testing.T) {
	// Let's create an even number of send batches so we don't run into the
	// batch timeout case.
	n := 100

	samples := make([]sample, 0, n)
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("test_metric_%d", i)
		samples = append(samples, sample{
			Name: name,
			Labels: map[string]string{
				model.InstanceLabel: strconv.Itoa(i),
			},
			Value:          float64(i),
			ResetTimestamp: 1234567890000,
			Timestamp:      2234567890000,
		})
	}

	c := NewTestStorageClient(t)
	m := NewQueueManager(nil, config.DefaultQueueConfig, c)

	// These should be received by the client.
	for _, s := range samples {
		metricFamily, err := retrieval.NewMetricFamily(
			&dto.MetricFamily{
				Name: proto.String(s.Name),
				Type: dto.MetricType_UNTYPED.Enum(),
				Metric: []*dto.Metric{
					&dto.Metric{
						Label: []*dto.LabelPair{
							&dto.LabelPair{
								Name:  proto.String(model.InstanceLabel),
								Value: proto.String("i123"),
							},
						},
					},
				},
			},
			[]int64{
				1234567890000,
			},
			labels.Labels{TestTargetLabel})
		if err != nil {
			panic(err)
		}
		m.Append(metricFamily)
	}
	m.Start()
	defer m.Stop()

	c.waitForExpectedSamples(t)
}

// TestBlockingStorageClient is a queue_manager StorageClient which will block
// on any calls to Store(), until the `block` channel is closed, at which point
// the `numCalls` property will contain a count of how many times Store() was
// called.
type TestBlockingStorageClient struct {
	numCalls uint64
	block    chan bool
}

func NewTestBlockedStorageClient() *TestBlockingStorageClient {
	return &TestBlockingStorageClient{
		block:    make(chan bool),
		numCalls: 0,
	}
}

func (c *TestBlockingStorageClient) Store(_ *monitoring.CreateTimeSeriesRequest) error {
	atomic.AddUint64(&c.numCalls, 1)
	<-c.block
	return nil
}

func (c *TestBlockingStorageClient) NumCalls() uint64 {
	return atomic.LoadUint64(&c.numCalls)
}

func (c *TestBlockingStorageClient) unlock() {
	close(c.block)
}

func (t *TestBlockingStorageClient) New() StorageClient {
	return t
}

func (c *TestBlockingStorageClient) Name() string {
	return "testblockingstorageclient"
}

func (c *TestBlockingStorageClient) Close() error {
	return nil
}

func (t *QueueManager) queueLen() int {
	t.shardsMtx.Lock()
	defer t.shardsMtx.Unlock()
	queueLength := 0
	for _, shard := range t.shards.shards {
		queueLength += len(shard.queue)
	}
	return queueLength
}

func TestSpawnNotMoreThanMaxConcurrentSendsGoroutines(t *testing.T) {
	// Our goal is to fully empty the queue:
	// `MaxSamplesPerSend*Shards` samples should be consumed by the
	// per-shard goroutines, and then another `MaxSamplesPerSend`
	// should be left on the queue.
	n := config.DefaultQueueConfig.MaxSamplesPerSend * 2

	samples := make([]sample, 0, n)
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("test_metric_%d", i)
		samples = append(samples, sample{
			Name:           name,
			Labels:         map[string]string{},
			Value:          float64(i),
			ResetTimestamp: 1234567890000,
			Timestamp:      2234567890000,
		})
	}

	c := NewTestBlockedStorageClient()
	cfg := config.DefaultQueueConfig
	cfg.MaxShards = 1
	cfg.Capacity = n
	m := NewQueueManager(nil, cfg, c)

	m.Start()

	defer func() {
		c.unlock()
		m.Stop()
	}()

	for _, s := range samples {
		m.Append(samplesToMetricFamily(s))
	}

	// Wait until the runShard() loops drain the queue.  If things went right, it
	// should then immediately block in sendSamples(), but, in case of error,
	// it would spawn too many goroutines, and thus we'd see more calls to
	// client.Store()
	//
	// The timed wait is maybe non-ideal, but, in order to verify that we're
	// not spawning too many concurrent goroutines, we have to wait on the
	// Run() loop to consume a specific number of elements from the
	// queue... and it doesn't signal that in any obvious way, except by
	// draining the queue.  We cap the waiting at 1 second -- that should give
	// plenty of time, and keeps the failure fairly quick if we're not draining
	// the queue properly.
	for i := 0; i < 100 && m.queueLen() > 0; i++ {
		time.Sleep(10 * time.Millisecond)
	}

	if m.queueLen() != config.DefaultQueueConfig.MaxSamplesPerSend {
		t.Fatalf("Failed to drain QueueManager queue, %d elements left",
			m.queueLen(),
		)
	}

	numCalls := c.NumCalls()
	if numCalls != uint64(1) {
		t.Errorf("Saw %d concurrent sends, expected 1", numCalls)
	}
}

// All samples must have the same Name.
func samplesToMetricFamily(samples ...sample) *retrieval.MetricFamily {
	metrics := make([]*dto.Metric, len(samples))
	resetTimestamps := make([]int64, len(samples))
	for i, _ := range samples {
		if samples[i].Name != samples[0].Name {
			panic("all metric family names in this call must match")
		}
		metricLabels := make([]*dto.LabelPair, 0)
		metricLabels = append(metricLabels, &dto.LabelPair{
			Name:  proto.String(ProjectIdLabel),
			Value: proto.String("1234567890"),
		})
		metricLabels = append(metricLabels,
			&dto.LabelPair{
				Name:  proto.String(model.InstanceLabel),
				Value: proto.String(strconv.Itoa(int(samples[0].Value))),
			})
		for ln, lv := range samples[i].Labels {
			metricLabels = append(metricLabels, &dto.LabelPair{
				Name:  proto.String(ln),
				Value: proto.String(lv),
			})
		}
		metrics[i] = &dto.Metric{
			Label: metricLabels,
			Counter: &dto.Counter{
				Value: proto.Float64(samples[i].Value),
			},
			TimestampMs: proto.Int64(samples[i].Timestamp),
		}
		resetTimestamps[i] = samples[i].ResetTimestamp
	}
	metricFamily, err := retrieval.NewMetricFamily(
		&dto.MetricFamily{
			Name:   proto.String(samples[0].Name),
			Type:   dto.MetricType_COUNTER.Enum(),
			Metric: metrics,
		},
		resetTimestamps,
		labels.Labels{
			TestTargetLabel,
			// This label isn't part of the monitored resource, so
			// two time series that only differ by this, will be
			// seen as one time series by Stackdriver. Samples
			// across those two time series are subject to Stackdriver
			// constraints, such as ordering.
			{"__not_in_monitored_resource", strconv.Itoa(int(samples[0].Value))},
		})
	if err != nil {
		panic(err)
	}
	return metricFamily
}
