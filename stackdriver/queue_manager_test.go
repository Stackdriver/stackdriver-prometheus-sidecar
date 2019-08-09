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
	"context"
	"fmt"
	"io/ioutil"
	"os"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Stackdriver/stackdriver-prometheus-sidecar/tail"
	timestamp_pb "github.com/golang/protobuf/ptypes/timestamp"
	"github.com/prometheus/common/model"
	"github.com/prometheus/prometheus/config"
	metric_pb "google.golang.org/genproto/googleapis/api/metric"
	monitoring_pb "google.golang.org/genproto/googleapis/monitoring/v3"
)

// TestStorageClient simulates a storage that can store samples and compares it
// with an expected set.
// All inserted series must be uniquely identified by their metric type string.
type TestStorageClient struct {
	receivedSamples map[string][]*monitoring_pb.TimeSeries
	expectedSamples map[string][]*monitoring_pb.TimeSeries
	wg              sync.WaitGroup
	mtx             sync.Mutex
	t               *testing.T
}

func newTestSample(name string, start, end int64, v float64) *monitoring_pb.TimeSeries {
	return &monitoring_pb.TimeSeries{
		Metric: &metric_pb.Metric{
			Type: name,
		},
		MetricKind: metric_pb.MetricDescriptor_GAUGE,
		ValueType:  metric_pb.MetricDescriptor_DOUBLE,
		Points: []*monitoring_pb.Point{{
			Interval: &monitoring_pb.TimeInterval{
				StartTime: &timestamp_pb.Timestamp{Seconds: start},
				EndTime:   &timestamp_pb.Timestamp{Seconds: end},
			},
			Value: &monitoring_pb.TypedValue{
				Value: &monitoring_pb.TypedValue_DoubleValue{v},
			},
		}},
	}
}

func NewTestStorageClient(t *testing.T) *TestStorageClient {
	return &TestStorageClient{
		receivedSamples: map[string][]*monitoring_pb.TimeSeries{},
		expectedSamples: map[string][]*monitoring_pb.TimeSeries{},
		t:               t,
	}
}

func (c *TestStorageClient) expectSamples(samples []*monitoring_pb.TimeSeries) {
	c.mtx.Lock()
	defer c.mtx.Unlock()
	for _, s := range samples {
		c.expectedSamples[s.Metric.Type] = append(c.expectedSamples[s.Metric.Type], s)
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
	c.receivedSamples = map[string][]*monitoring_pb.TimeSeries{}
	c.expectedSamples = map[string][]*monitoring_pb.TimeSeries{}
}

func (c *TestStorageClient) Store(req *monitoring_pb.CreateTimeSeriesRequest) error {
	c.mtx.Lock()
	defer c.mtx.Unlock()

	for i, ts := range req.TimeSeries {
		for _, prev := range req.TimeSeries[:i] {
			if reflect.DeepEqual(prev, ts) {
				c.t.Fatalf("found duplicate time series in request: %v", ts)
			}
		}
		if len(ts.Points) != 1 {
			c.t.Fatalf("unexpected number of points %d", len(ts.Points))
		}
		c.receivedSamples[ts.Metric.Type] = append(c.receivedSamples[ts.Metric.Type], ts)
		c.wg.Done()
	}
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

func TestSampleDeliverySimple(t *testing.T) {
	dir, err := ioutil.TempDir("", "test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	// Let's create an even number of send batches so we don't run into the
	// batch timeout case.
	n := 100

	var samples []*monitoring_pb.TimeSeries
	for i := 0; i < n; i++ {
		samples = append(samples, newTestSample(
			fmt.Sprintf("test_metric_%d", i),
			1234567890000,
			2234567890000,
			float64(i),
		))
	}

	c := NewTestStorageClient(t)
	c.expectSamples(samples)

	cfg := config.DefaultQueueConfig
	cfg.Capacity = n
	cfg.MaxSamplesPerSend = n

	tailer, err := tail.Tail(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	m, err := NewQueueManager(nil, cfg, c, tailer)
	if err != nil {
		t.Fatal(err)
	}

	// These should be received by the client.
	for i, s := range samples {
		m.Append(uint64(i), s)
	}
	m.Start()
	defer m.Stop()

	c.waitForExpectedSamples(t)
}

func TestSampleDeliveryMultiShard(t *testing.T) {
	dir, err := ioutil.TempDir("", "test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	numShards := 10
	n := 5 * numShards

	var samples []*monitoring_pb.TimeSeries
	for i := 0; i < n; i++ {
		samples = append(samples, newTestSample(
			fmt.Sprintf("test_metric_%d", i),
			1234567890000,
			2234567890000,
			float64(i),
		))
	}

	c := NewTestStorageClient(t)

	cfg := config.DefaultQueueConfig
	// flush after each sample, to avoid blocking the test
	cfg.MaxSamplesPerSend = 1
	cfg.MaxShards = numShards

	tailer, err := tail.Tail(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	m, err := NewQueueManager(nil, cfg, c, tailer)
	if err != nil {
		t.Fatal(err)
	}

	m.Start()
	defer m.Stop()
	m.reshard(numShards) // blocks until resharded

	c.expectSamples(samples)
	// These should be received by the client.
	for i, s := range samples {
		m.Append(uint64(i), s)
	}

	c.waitForExpectedSamples(t)
}

func TestSampleDeliveryTimeout(t *testing.T) {
	dir, err := ioutil.TempDir("", "test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	// Let's send one less sample than batch size, and wait the timeout duration
	n := config.DefaultQueueConfig.MaxSamplesPerSend - 1

	var samples1, samples2 []*monitoring_pb.TimeSeries
	for i := 0; i < n; i++ {
		samples1 = append(samples1, newTestSample(
			fmt.Sprintf("test_metric_%d", i),
			1234567890000,
			2234567890000,
			float64(i),
		))
		samples2 = append(samples2, newTestSample(
			fmt.Sprintf("test_metric_%d", i),
			1234567890000,
			2234567890000+1,
			float64(i),
		))

	}

	c := NewTestStorageClient(t)
	cfg := config.DefaultQueueConfig
	cfg.MaxShards = 1
	cfg.BatchSendDeadline = model.Duration(100 * time.Millisecond)

	tailer, err := tail.Tail(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	m, err := NewQueueManager(nil, cfg, c, tailer)
	if err != nil {
		t.Fatal(err)
	}

	m.Start()
	defer m.Stop()

	// Send the samples twice, waiting for the samples in the meantime.
	c.expectSamples(samples1)
	for i, s := range samples1 {
		m.Append(uint64(i), s)
	}
	c.waitForExpectedSamples(t)

	c.resetExpectedSamples()
	c.expectSamples(samples2)

	for i, s := range samples2 {
		m.Append(uint64(i), s)
	}
	c.waitForExpectedSamples(t)
}

func TestSampleDeliveryOrder(t *testing.T) {
	dir, err := ioutil.TempDir("", "test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	ts := 10
	n := config.DefaultQueueConfig.MaxSamplesPerSend * ts

	var samples []*monitoring_pb.TimeSeries
	for i := 0; i < n; i++ {
		samples = append(samples, newTestSample(
			fmt.Sprintf("test_metric_%d", i%ts),
			1234567890001,
			1234567890001+int64(i),
			float64(i),
		))
	}

	c := NewTestStorageClient(t)
	c.expectSamples(samples)

	tailer, err := tail.Tail(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	m, err := NewQueueManager(nil, config.DefaultQueueConfig, c, tailer)
	if err != nil {
		t.Fatal(err)
	}

	m.Start()
	defer m.Stop()
	// These should be received by the client.
	for i, s := range samples {
		m.Append(uint64(i), s)
	}

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

func (c *TestBlockingStorageClient) Store(_ *monitoring_pb.CreateTimeSeriesRequest) error {
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
	dir, err := ioutil.TempDir("", "test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	// Our goal is to fully empty the queue:
	// `MaxSamplesPerSend*Shards` samples should be consumed by the
	// per-shard goroutines, and then another `MaxSamplesPerSend`
	// should be left on the queue.
	n := config.DefaultQueueConfig.MaxSamplesPerSend * 2

	var samples []*monitoring_pb.TimeSeries
	for i := 0; i < n; i++ {
		samples = append(samples, newTestSample(
			fmt.Sprintf("test_metric_%d", i),
			1234567890001,
			2234567890001,
			float64(i),
		))
	}

	c := NewTestBlockedStorageClient()
	cfg := config.DefaultQueueConfig
	cfg.MaxShards = 1
	cfg.Capacity = n

	tailer, err := tail.Tail(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	m, err := NewQueueManager(nil, cfg, c, tailer)
	if err != nil {
		t.Fatal(err)
	}

	m.Start()

	defer func() {
		c.unlock()
		m.Stop()
	}()

	for i, s := range samples {
		m.Append(uint64(i), s)
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
		t.Errorf("Failed to drain QueueManager queue, %d elements left",
			m.queueLen(),
		)
	}

	numCalls := c.NumCalls()
	if numCalls != uint64(1) {
		t.Errorf("Saw %d concurrent sends, expected 1", numCalls)
	}
}
