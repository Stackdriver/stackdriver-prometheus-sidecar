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
	"math"
	"sync"
	"time"

	"github.com/Stackdriver/stackdriver-prometheus-sidecar/relabel"
	"github.com/Stackdriver/stackdriver-prometheus-sidecar/retrieval"
	"github.com/go-kit/kit/log"
	"github.com/go-kit/kit/log/level"
	"github.com/gogo/protobuf/proto"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/model"
	"github.com/prometheus/prometheus/config"
	"golang.org/x/time/rate"
	monitoring "google.golang.org/genproto/googleapis/monitoring/v3"
)

// String constants for instrumentation.
const (
	namespace = "prometheus"
	subsystem = "remote_storage"
	queue     = "queue"

	// We track samples in/out and how long pushes take using an Exponentially
	// Weighted Moving Average.
	ewmaWeight          = 0.2
	shardUpdateDuration = 10 * time.Second

	// Allow 10% too many shards before scaling down.
	shardToleranceFraction = 0.1

	// Limit to 1 log event every 10s
	logRateLimit = 0.1
	logBurst     = 10
)

var (
	succeededSamplesTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: namespace,
			Subsystem: subsystem,
			Name:      "succeeded_samples_total",
			Help:      "Total number of samples successfully sent to remote storage.",
		},
		[]string{queue},
	)
	failedSamplesTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: namespace,
			Subsystem: subsystem,
			Name:      "failed_samples_total",
			Help:      "Total number of samples which failed on send to remote storage.",
		},
		[]string{queue},
	)
	droppedSamplesTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: namespace,
			Subsystem: subsystem,
			Name:      "dropped_samples_total",
			Help:      "Total number of samples which were dropped due to the queue being full.",
		},
		[]string{queue},
	)
	sentBatchDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: namespace,
			Subsystem: subsystem,
			Name:      "sent_batch_duration_seconds",
			Help:      "Duration of sample batch send calls to the remote storage.",
			Buckets:   prometheus.DefBuckets,
		},
		[]string{queue},
	)
	queueLength = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: namespace,
			Subsystem: subsystem,
			Name:      "queue_length",
			Help:      "The number of processed samples queued to be sent to the remote storage.",
		},
		[]string{queue},
	)
	queueCapacity = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: namespace,
			Subsystem: subsystem,
			Name:      "queue_capacity",
			Help:      "The capacity of the queue of samples to be sent to the remote storage.",
		},
		[]string{queue},
	)
	numShards = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: namespace,
			Subsystem: subsystem,
			Name:      "shards",
			Help:      "The number of shards used for parallel sending to the remote storage.",
		},
		[]string{queue},
	)
)

func init() {
	prometheus.MustRegister(succeededSamplesTotal)
	prometheus.MustRegister(failedSamplesTotal)
	prometheus.MustRegister(droppedSamplesTotal)
	prometheus.MustRegister(sentBatchDuration)
	prometheus.MustRegister(queueLength)
	prometheus.MustRegister(queueCapacity)
	prometheus.MustRegister(numShards)
}

// StorageClient defines an interface for sending a batch of samples to an
// external timeseries database.
type StorageClient interface {
	// Store stores the given metric families in the remote storage.
	Store(*monitoring.CreateTimeSeriesRequest) error
	// Name identifies the remote storage implementation.
	Name() string
	// Release the resources allocated by the client.
	Close() error
}

type StorageClientFactory interface {
	New() StorageClient
	Name() string
}

// QueueManager manages a queue of samples to be sent to the Storage
// indicated by the provided StorageClient.
type QueueManager struct {
	logger log.Logger

	cfg              config.QueueConfig
	externalLabels   map[string]*string
	relabelConfigs   []*config.RelabelConfig
	clientFactory    StorageClientFactory
	queueName        string
	logLimiter       *rate.Limiter
	resourceMappings []ResourceMap

	shardsMtx   sync.RWMutex
	shards      *shardCollection
	numShards   int
	reshardChan chan int
	quit        chan struct{}
	wg          sync.WaitGroup

	samplesIn *ewmaRate
}

// NewQueueManager builds a new QueueManager.
func NewQueueManager(logger log.Logger, cfg config.QueueConfig, externalLabelSet model.LabelSet, relabelConfigs []*config.RelabelConfig, clientFactory StorageClientFactory, sdCfg *StackdriverConfig) *QueueManager {
	if logger == nil {
		logger = log.NewNopLogger()
	}
	resourceMappings := DefaultResourceMappings
	externalLabels := make(map[string]*string, len(externalLabelSet))
	for ln, lv := range externalLabelSet {
		externalLabels[string(ln)] = proto.String(string(lv))
	}
	t := &QueueManager{
		logger:           logger,
		cfg:              cfg,
		externalLabels:   externalLabels,
		relabelConfigs:   relabelConfigs,
		clientFactory:    clientFactory,
		queueName:        clientFactory.Name(),
		resourceMappings: resourceMappings,

		logLimiter:  rate.NewLimiter(logRateLimit, logBurst),
		numShards:   1,
		reshardChan: make(chan int),
		quit:        make(chan struct{}),

		samplesIn: newEWMARate(ewmaWeight, shardUpdateDuration),
	}
	t.shards = t.newShardCollection(t.numShards)
	numShards.WithLabelValues(t.queueName).Set(float64(t.numShards))
	queueCapacity.WithLabelValues(t.queueName).Set(float64(t.cfg.Capacity))

	// Initialise counter labels to zero.
	sentBatchDuration.WithLabelValues(t.queueName)
	succeededSamplesTotal.WithLabelValues(t.queueName)
	failedSamplesTotal.WithLabelValues(t.queueName)
	droppedSamplesTotal.WithLabelValues(t.queueName)

	return t
}

// Append queues a sample to be sent to the remote storage. It drops the
// sample on the floor if the queue is full.
// Always returns nil.
func (t *QueueManager) Append(metricFamily *retrieval.MetricFamily) error {
	metricFamily = t.relabelMetrics(metricFamily)
	// Drop family if we dropped all metrics.
	if metricFamily.Metric == nil {
		return nil
	}
	if len(metricFamily.Metric) != len(metricFamily.MetricResetTimestampMs) {
		level.Error(t.logger).Log(
			"msg", "bug: number of metrics and reset timestamps must match",
			"metrics", len(metricFamily.Metric),
			"reset_ts", len(metricFamily.MetricResetTimestampMs))
	}

	queueLength.WithLabelValues(t.queueName).Add(float64(len(metricFamily.Metric)))
	t.shardsMtx.RLock()
	for i := range metricFamily.Metric {
		t.shards.enqueue(metricFamily.Slice(i))
	}
	t.shardsMtx.RUnlock()
	return nil
}

// Start the queue manager sending samples to the remote storage.
// Does not block.
func (t *QueueManager) Start() {
	t.wg.Add(2)
	go t.updateShardsLoop()
	go t.reshardLoop()

	t.shardsMtx.Lock()
	defer t.shardsMtx.Unlock()
	t.shards.start()
}

// Stop stops sending samples to the remote storage and waits for pending
// sends to complete.
func (t *QueueManager) Stop() {
	level.Info(t.logger).Log("msg", "Stopping remote storage...")
	close(t.quit)
	t.wg.Wait()

	t.shardsMtx.Lock()
	defer t.shardsMtx.Unlock()
	t.shards.stop()

	level.Info(t.logger).Log("msg", "Remote storage stopped.")
}

func (t *QueueManager) updateShardsLoop() {
	defer t.wg.Done()

	ticker := time.NewTicker(shardUpdateDuration)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			t.calculateDesiredShards()
		case <-t.quit:
			return
		}
	}
}

func (t *QueueManager) calculateDesiredShards() {
	t.samplesIn.tick()

	// We use the number of incoming samples as a prediction of how much work we
	// will need to do next iteration.  We add to this any pending samples
	// (received - send) so we can catch up with any backlog. We use the average
	// outgoing batch latency to work out how many shards we need.
	// These rates are samples per second.
	samplesIn := t.samplesIn.rate()

	// Size the shards so that they can fit enough samples to keep
	// good flow, but not more than the batch send deadline,
	// otherwise we'll underutilize the batches. Below "send" is for one batch.
	desiredShards := t.cfg.BatchSendDeadline.Seconds() * samplesIn / float64(t.cfg.Capacity)

	level.Debug(t.logger).Log("msg", "QueueManager.caclulateDesiredShards",
		"samplesIn", samplesIn, "desiredShards", desiredShards)

	// Changes in the number of shards must be greater than shardToleranceFraction.
	var (
		lowerBound = float64(t.numShards) * (1. - shardToleranceFraction)
		upperBound = float64(t.numShards) * (1. + shardToleranceFraction)
	)
	level.Debug(t.logger).Log("msg", "QueueManager.updateShardsLoop",
		"lowerBound", lowerBound, "desiredShards", desiredShards, "upperBound", upperBound)
	if lowerBound <= desiredShards && desiredShards <= upperBound {
		return
	}

	numShards := int(math.Ceil(desiredShards))
	if numShards > t.cfg.MaxShards {
		numShards = t.cfg.MaxShards
	} else if numShards < 1 {
		numShards = 1
	}
	if numShards == t.numShards {
		return
	}

	// Resharding can take some time, and we want this loop
	// to stay close to shardUpdateDuration.
	select {
	case t.reshardChan <- numShards:
		level.Debug(t.logger).Log("msg", "Remote storage resharding", "from", t.numShards, "to", numShards)
		t.numShards = numShards
	default:
		level.Debug(t.logger).Log("msg", "Currently resharding, skipping", "to", numShards)
	}
}

func (t *QueueManager) reshardLoop() {
	defer t.wg.Done()

	for {
		select {
		case numShards := <-t.reshardChan:
			t.reshard(numShards)
		case <-t.quit:
			return
		}
	}
}

func (t *QueueManager) reshard(n int) {
	numShards.WithLabelValues(t.queueName).Set(float64(n))

	t.shardsMtx.Lock()
	newShards := t.newShardCollection(n)
	oldShards := t.shards
	t.shards = newShards

	// Carry over the map of youngest samples to the new shards.
	//
	// Reset timestamps are determined in the scraper, and in case multiple
	// scrapers collect the same time series (e.g. overlapping scrape
	// configs), cumulative samples from different scrapers are likely to
	// have overlapping intervals. Normally this map guarantees
	// non-overlapping intervals in the output, but if after resharding the
	// map gets initialized with an earlier reset timestamp, the overlapping
	// intervals will prevent the right samples from making it to
	// Stackdriver, causing the time series to get stuck.  This could be
	// avoided by aligning reset timestamps across all shards, e.g. using
	// DELTA metrics instead of CUMULATIVE.
	oldShards.stop() // the old shards must be stopped before we poke at their internals
	// This is a critical section, and growing the map on a large deployment
	// is slow, so preallocate the map to be near the expected size.
	for newShardIndex := range newShards.shards {
		newShards.shards[newShardIndex].youngestSampleIntervals = make(map[uint64]sampleInterval, len(oldShards.shards[0].youngestSampleIntervals))
	}
	newShardsModulo := uint64(len(newShards.shards))
	for oldShardIndex := range oldShards.shards {
		for k, v := range oldShards.shards[oldShardIndex].youngestSampleIntervals {
			newShardIndex := k % newShardsModulo
			newShards.shards[newShardIndex].youngestSampleIntervals[k] = v
		}
	}
	t.shardsMtx.Unlock()

	// We start the newShards after we have stopped (the therefore completely
	// flushed) the oldShards, to guarantee we only every deliver samples in
	// order.
	newShards.start()
}

type sampleInterval struct {
	resetTimestamp int64
	timestamp      int64
}

// AcceptsInterval returns true if the given interval can be written to
// Stackdriver after the reference interval. This defines an order for value
// timestamps with equal reset timestamp, and requires that if the reset
// timestamp moves, it doesn't overlap with the previous interval.
func (si *sampleInterval) AcceptsInterval(o sampleInterval) bool {
	return (o.resetTimestamp == si.resetTimestamp && o.timestamp > si.timestamp) ||
		(o.resetTimestamp > si.resetTimestamp && o.resetTimestamp >= si.timestamp)
}

type shard struct {
	queue                   chan *retrieval.MetricFamily
	youngestSampleIntervals map[uint64]sampleInterval
}

func newShard(cfg config.QueueConfig) shard {
	return shard{
		queue: make(chan *retrieval.MetricFamily, cfg.Capacity),
		youngestSampleIntervals: map[uint64]sampleInterval{},
	}
}

type shardCollection struct {
	qm         *QueueManager
	client     StorageClient
	translator *Translator
	shards     []shard
	done       chan struct{}
	wg         sync.WaitGroup
}

func (t *QueueManager) newShardCollection(numShards int) *shardCollection {
	shards := make([]shard, numShards)
	for i := 0; i < numShards; i++ {
		shards[i] = newShard(t.cfg)
	}
	s := &shardCollection{
		qm:         t,
		translator: NewTranslator(t.logger, metricsPrefix, t.resourceMappings),
		shards:     shards,
		done:       make(chan struct{}),
	}
	s.wg.Add(numShards)
	return s
}

func (s *shardCollection) len() int {
	return len(s.shards)
}

func (s *shardCollection) start() {
	for i := range s.shards {
		go s.runShard(i)
	}
}

func (s *shardCollection) stop() {
	for _, shard := range s.shards {
		close(shard.queue)
	}
	s.wg.Wait()
	level.Debug(s.qm.logger).Log("msg", "Stopped resharding")
}

func (s *shardCollection) enqueue(sample *retrieval.MetricFamily) {
	s.qm.samplesIn.incr(1)

	fp := sample.Fingerprint()
	shardIndex := fp % uint64(len(s.shards))
	s.shards[shardIndex].queue <- sample
}

func (s *shardCollection) runShard(i int) {
	defer s.wg.Done()
	client := s.qm.clientFactory.New()
	defer client.Close()
	shard := s.shards[i]

	// Send batches of at most MaxSamplesPerSend samples to the remote storage.
	// If we have fewer samples than that, flush them out after a deadline
	// anyways.
	pendingSamples := make([]*retrieval.MetricFamily, 0, s.qm.cfg.MaxSamplesPerSend)
	// Fingerprint of time series contained in pendingSamples. Gets reset
	// whenever samples are extracted from pendingSamples.
	newSeenSamples := func() map[uint64]struct{} {
		return make(map[uint64]struct{}, s.qm.cfg.MaxSamplesPerSend)
	}
	seenSamples := newSeenSamples()

	timer := time.NewTimer(s.qm.cfg.BatchSendDeadline)
	stop := func() {
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
	}
	defer stop()

	for {
		select {
		case sample, ok := <-shard.queue:
			if !ok {
				if len(pendingSamples) > 0 {
					level.Debug(s.qm.logger).Log("msg", "Flushing samples to remote storage...", "count", len(pendingSamples))
					s.sendSamples(client, pendingSamples)
					level.Debug(s.qm.logger).Log("msg", "Done flushing.")
				}
				return
			}
			queueLength.WithLabelValues(s.qm.queueName).Dec()

			// Stackdriver rejects requests with points out of order
			// or with multiple points for the same time series. The
			// check below reduces the likelihood that this will
			// happen by keeping track of the youngest interval
			// written for each time series by the current
			// shard. Each shard builds requests for a disjoint set
			// of time series, so we don't need to track the
			// intervals across shards.
			fp := sample.Fingerprint()
			if len(sample.Metric) != 1 || len(sample.MetricResetTimestampMs) != 1 {
				panic("expected one metric")
			}
			currentSampleInterval := sampleInterval{
				resetTimestamp: sample.MetricResetTimestampMs[0],
				timestamp:      sample.Metric[0].GetTimestampMs(),
			}
			if youngestSampleInterval, ok := shard.youngestSampleIntervals[fp]; !ok || youngestSampleInterval.AcceptsInterval(currentSampleInterval) {
				shard.youngestSampleIntervals[fp] = currentSampleInterval

				// If pendingSamples contains a point for the
				// incoming time series, send all pending points
				// to Stackdriver, and start a new list. This
				// prevents adding two points for the same time
				// series to a single request, which Stackdriver
				// rejects.
				_, seen := seenSamples[fp]
				if !seen {
					pendingSamples = append(pendingSamples, sample)
					seenSamples[fp] = struct{}{}
				}
				if len(pendingSamples) >= s.qm.cfg.MaxSamplesPerSend || seen {
					s.sendSamples(client, pendingSamples)
					pendingSamples = pendingSamples[:0]
					seenSamples = newSeenSamples()

					stop()
					timer.Reset(s.qm.cfg.BatchSendDeadline)
				}
				if seen {
					pendingSamples = append(pendingSamples, sample)
					seenSamples[fp] = struct{}{}
				}
			}
		case <-timer.C:
			if len(pendingSamples) > 0 {
				s.sendSamples(client, pendingSamples)
				pendingSamples = pendingSamples[:0]
				seenSamples = newSeenSamples()
			}
			timer.Reset(s.qm.cfg.BatchSendDeadline)
		}
	}
}

// sendSamples to the remote storage with backoff for recoverable errors.
func (s *shardCollection) sendSamples(client StorageClient, samples []*retrieval.MetricFamily) {
	req := s.translator.ToCreateTimeSeriesRequest(samples)
	backoff := s.qm.cfg.MinBackoff
	for retries := s.qm.cfg.MaxRetries; retries > 0; retries-- {
		begin := time.Now()
		err := client.Store(req)

		sentBatchDuration.WithLabelValues(s.qm.queueName).Observe(time.Since(begin).Seconds())
		if err == nil {
			succeededSamplesTotal.WithLabelValues(s.qm.queueName).Add(float64(len(samples)))
			return
		}

		if _, ok := err.(recoverableError); !ok {
			level.Warn(s.qm.logger).Log("msg", "Unrecoverable error sending samples to remote storage", "err", err)
			break
		}
		time.Sleep(backoff)
		backoff = backoff * 2
		if backoff > s.qm.cfg.MaxBackoff {
			backoff = s.qm.cfg.MaxBackoff
		}
	}

	failedSamplesTotal.WithLabelValues(s.qm.queueName).Add(float64(len(samples)))
}

// relabelMetrics returns nil if the relabeling requested the metric to be dropped.
func (t *QueueManager) relabelMetrics(metricFamily *retrieval.MetricFamily) *retrieval.MetricFamily {
	metrics := []*dto.Metric{}
	resetTimestamps := []int64{}
	for i, metric := range metricFamily.Metric {
		if metric.Label == nil {
			// Need to distinguish dropped labels from uninitialized.
			metric.Label = []*dto.LabelPair{}
		}
		// Add any external labels. If an external label name is already
		// found in the set of metric labels, don't add that label.
		lset := make(map[string]struct{})
		for i := range metric.Label {
			lset[*metric.Label[i].Name] = struct{}{}
		}
		for ln, lv := range t.externalLabels {
			if _, ok := lset[ln]; !ok {
				metric.Label = append(metric.Label, &dto.LabelPair{
					Name:  proto.String(ln),
					Value: lv,
				})
			}
		}

		metric.Label = relabel.Process(metric.Label, t.relabelConfigs...)
		// The label set may be set to nil to indicate dropping.
		if metric.Label != nil {
			if len(metric.Label) == 0 {
				metric.Label = nil
			}
			metrics = append(metrics, metric)
			resetTimestamps = append(resetTimestamps, metricFamily.MetricResetTimestampMs[i])
		}
	}
	metricFamily.Metric = metrics
	metricFamily.MetricResetTimestampMs = resetTimestamps
	return metricFamily
}
