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

	"github.com/go-kit/kit/log"
	"github.com/go-kit/kit/log/level"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/prometheus/config"
	"golang.org/x/time/rate"
	monitoring_pb "google.golang.org/genproto/googleapis/monitoring/v3"
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
	Store(*monitoring_pb.CreateTimeSeriesRequest) error
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

	cfg           config.QueueConfig
	clientFactory StorageClientFactory
	queueName     string
	logLimiter    *rate.Limiter

	shardsMtx   sync.RWMutex
	shards      *shardCollection
	numShards   int
	reshardChan chan int
	quit        chan struct{}
	wg          sync.WaitGroup

	samplesIn *ewmaRate
}

// NewQueueManager builds a new QueueManager.
func NewQueueManager(logger log.Logger, cfg config.QueueConfig, clientFactory StorageClientFactory) *QueueManager {
	if logger == nil {
		logger = log.NewNopLogger()
	}
	t := &QueueManager{
		logger:        logger,
		cfg:           cfg,
		clientFactory: clientFactory,
		queueName:     clientFactory.Name(),

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
func (t *QueueManager) Append(hash uint64, sample *monitoring_pb.TimeSeries) error {
	queueLength.WithLabelValues(t.queueName).Inc()
	t.shardsMtx.RLock()
	t.shards.enqueue(hash, sample)
	t.shardsMtx.RUnlock()
	return nil
}

// Start the queue manager sending samples to the remote storage.
// Does not block.
func (t *QueueManager) Start() error {
	t.wg.Add(2)
	go t.updateShardsLoop()
	go t.reshardLoop()

	t.shardsMtx.Lock()
	defer t.shardsMtx.Unlock()
	t.shards.start()

	return nil
}

// Stop stops sending samples to the remote storage and waits for pending
// sends to complete.
func (t *QueueManager) Stop() error {
	level.Info(t.logger).Log("msg", "Stopping remote storage...")
	close(t.quit)
	t.wg.Wait()

	t.shardsMtx.Lock()
	defer t.shardsMtx.Unlock()
	t.shards.stop()

	level.Info(t.logger).Log("msg", "Remote storage stopped.")
	return nil
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
	oldShards.stop()
	t.shardsMtx.Unlock()

	// We start the newShards after we have stopped (the therefore completely
	// flushed) the oldShards, to guarantee we only every deliver samples in
	// order.
	newShards.start()
}

type queueEntry struct {
	hash   uint64
	sample *monitoring_pb.TimeSeries
}

type shard struct {
	queue chan queueEntry
	// A reusable cache of samples that were already seen in a
	// smaple batch.
	seen map[uint64]struct{}
}

func (s *shard) resetSeen() {
	for k := range s.seen {
		delete(s.seen, k)
	}
}

func newShard(cfg config.QueueConfig) shard {
	return shard{
		queue: make(chan queueEntry, cfg.Capacity),
		seen:  map[uint64]struct{}{},
	}
}

type shardCollection struct {
	qm     *QueueManager
	client StorageClient
	shards []shard
	done   chan struct{}
	wg     sync.WaitGroup
}

func (t *QueueManager) newShardCollection(numShards int) *shardCollection {
	shards := make([]shard, numShards)
	for i := 0; i < numShards; i++ {
		shards[i] = newShard(t.cfg)
	}
	s := &shardCollection{
		qm:     t,
		shards: shards,
		done:   make(chan struct{}),
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

func (s *shardCollection) enqueue(hash uint64, sample *monitoring_pb.TimeSeries) {
	s.qm.samplesIn.incr(1)
	shardIndex := hash % uint64(len(s.shards))
	s.shards[shardIndex].queue <- queueEntry{sample: sample, hash: hash}
}

func (s *shardCollection) runShard(i int) {
	defer s.wg.Done()
	client := s.qm.clientFactory.New()
	defer client.Close()
	shard := s.shards[i]

	// Send batches of at most MaxSamplesPerSend samples to the remote storage.
	// If we have fewer samples than that, flush them out after a deadline
	// anyways.
	pendingSamples := make([]*monitoring_pb.TimeSeries, 0, s.qm.cfg.MaxSamplesPerSend)
	// Fingerprint of time series contained in pendingSamples. Gets reset
	// whenever samples are extracted from pendingSamples.
	shard.resetSeen()

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
		case entry, ok := <-shard.queue:
			fp, sample := entry.hash, entry.sample

			if !ok {
				if len(pendingSamples) > 0 {
					// level.Debug(s.qm.logger).Log("msg", "Flushing samples to remote storage...", "count", len(pendingSamples))
					s.sendSamples(client, pendingSamples)
					// level.Debug(s.qm.logger).Log("msg", "Done flushing.")
				}
				return
			}
			queueLength.WithLabelValues(s.qm.queueName).Dec()

			// If pendingSamples contains a point for the
			// incoming time series, send all pending points
			// to Stackdriver, and start a new list. This
			// prevents adding two points for the same time
			// series to a single request, which Stackdriver
			// rejects.
			_, seen := shard.seen[fp]
			if !seen {
				pendingSamples = append(pendingSamples, sample)
				shard.seen[fp] = struct{}{}
			}
			if len(pendingSamples) >= s.qm.cfg.MaxSamplesPerSend || seen {
				s.sendSamples(client, pendingSamples)
				pendingSamples = pendingSamples[:0]
				shard.resetSeen()

				stop()
				timer.Reset(s.qm.cfg.BatchSendDeadline)
			}
			if seen {
				pendingSamples = append(pendingSamples, sample)
				shard.seen[fp] = struct{}{}
			}
		case <-timer.C:
			if len(pendingSamples) > 0 {
				s.sendSamples(client, pendingSamples)
				pendingSamples = pendingSamples[:0]
				shard.resetSeen()
			}
			timer.Reset(s.qm.cfg.BatchSendDeadline)
		}
	}
}

// sendSamples to the remote storage with backoff for recoverable errors.
func (s *shardCollection) sendSamples(client StorageClient, samples []*monitoring_pb.TimeSeries) {
	backoff := s.qm.cfg.MinBackoff
	for retries := s.qm.cfg.MaxRetries; retries > 0; retries-- {
		begin := time.Now()
		err := client.Store(&monitoring_pb.CreateTimeSeriesRequest{TimeSeries: samples})

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
