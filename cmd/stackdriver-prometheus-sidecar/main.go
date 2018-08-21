// Copyright 2015 The Prometheus Authors
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

// The main package for the Prometheus server executable.
package main

import (
	"context"
	"fmt"
	"net/http"
	_ "net/http/pprof" // Comment this line to disable pprof endpoint.
	"net/url"
	"os"
	"os/signal"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"syscall"
	"time"

	kingpin "gopkg.in/alecthomas/kingpin.v2"

	"github.com/go-kit/kit/log"
	"github.com/go-kit/kit/log/level"
	conntrack "github.com/mwitkow/go-conntrack"
	"github.com/oklog/oklog/pkg/group"
	"github.com/pkg/errors"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/prometheus/common/version"
	"github.com/prometheus/prometheus/config"
	"github.com/prometheus/prometheus/pkg/labels"
	oc_prometheus "go.opencensus.io/exporter/prometheus"
	"go.opencensus.io/plugin/ocgrpc"
	"go.opencensus.io/plugin/ochttp"
	"go.opencensus.io/stats/view"
	"go.opencensus.io/tag"

	"github.com/Stackdriver/stackdriver-prometheus-sidecar/metadata"
	"github.com/Stackdriver/stackdriver-prometheus-sidecar/retrieval"
	"github.com/Stackdriver/stackdriver-prometheus-sidecar/stackdriver"
	"github.com/Stackdriver/stackdriver-prometheus-sidecar/tail"
	"github.com/Stackdriver/stackdriver-prometheus-sidecar/targets"
	"github.com/prometheus/common/promlog"
	promlogflag "github.com/prometheus/common/promlog/flag"
)

var (
	sizeDistribution    = view.Distribution(0, 1024, 2048, 4096, 16384, 65536, 262144, 1048576, 4194304, 33554432)
	latencyDistribution = view.Distribution(0, 1, 2, 5, 10, 15, 25, 50, 100, 200, 400, 800, 1500, 3000, 6000)
)

func init() {
	prometheus.MustRegister(version.NewCollector("prometheus"))

	if err := view.Register(
		&view.View{
			Name:        "opencensus.io/http/client/request_count",
			Description: "Count of HTTP requests started",
			Measure:     ochttp.ClientRequestCount,
			TagKeys:     []tag.Key{ochttp.Method, ochttp.Path},
			Aggregation: view.Count(),
		},
		&view.View{
			Name:        "opencensus.io/http/client/request_bytes",
			Description: "Size distribution of HTTP request body",
			Measure:     ochttp.ClientRequestBytes,
			TagKeys:     []tag.Key{ochttp.Method, ochttp.StatusCode, ochttp.Path},
			Aggregation: sizeDistribution,
		},
		&view.View{
			Name:        "opencensus.io/http/client/response_bytes",
			Description: "Size distribution of HTTP response body",
			Measure:     ochttp.ClientResponseBytes,
			TagKeys:     []tag.Key{ochttp.Method, ochttp.StatusCode, ochttp.Path},
			Aggregation: sizeDistribution,
		},
		&view.View{
			Name:        "opencensus.io/http/client/latency",
			Description: "Latency distribution of HTTP requests",
			TagKeys:     []tag.Key{ochttp.Method, ochttp.StatusCode, ochttp.Path},
			Measure:     ochttp.ClientLatency,
			Aggregation: latencyDistribution,
		},
	); err != nil {
		panic(err)
	}
	if err := view.Register(
		&view.View{
			Measure:     ocgrpc.ClientSentBytesPerRPC,
			Name:        "grpc.io/client/sent_bytes_per_rpc",
			Description: "Distribution of bytes sent per RPC, by method.",
			TagKeys:     []tag.Key{ocgrpc.KeyClientMethod, ocgrpc.KeyClientStatus},
			Aggregation: sizeDistribution,
		},
		&view.View{
			Measure:     ocgrpc.ClientReceivedBytesPerRPC,
			Name:        "grpc.io/client/received_bytes_per_rpc",
			Description: "Distribution of bytes received per RPC, by method.",
			TagKeys:     []tag.Key{ocgrpc.KeyClientMethod, ocgrpc.KeyClientStatus},
			Aggregation: sizeDistribution,
		},
		&view.View{
			Measure:     ocgrpc.ClientRoundtripLatency,
			Name:        "grpc.io/client/roundtrip_latency",
			Description: "Distribution of round-trip latency, by method.",
			TagKeys:     []tag.Key{ocgrpc.KeyClientMethod, ocgrpc.KeyClientStatus},
			Aggregation: latencyDistribution,
		},
		&view.View{
			Measure:     ocgrpc.ClientRoundtripLatency,
			Name:        "grpc.io/client/completed_rpcs",
			Description: "Count of RPCs by method and status.",
			TagKeys:     []tag.Key{ocgrpc.KeyClientMethod, ocgrpc.KeyClientStatus},
			Aggregation: view.Count(),
		},
		&view.View{
			Measure:     ocgrpc.ClientServerLatency,
			Name:        "grpc.io/client/server_latency",
			Description: "Distribution of server latency as viewed by client, by method.",
			TagKeys:     []tag.Key{ocgrpc.KeyClientMethod, ocgrpc.KeyClientStatus},
			Aggregation: latencyDistribution,
		},
	); err != nil {
		panic(err)
	}
}

type kubernetesConfig struct {
	location    string
	clusterName string
}

func main() {
	if os.Getenv("DEBUG") != "" {
		runtime.SetBlockProfileRate(20)
		runtime.SetMutexProfileFraction(20)
	}

	cfg := struct {
		projectIdResource  string
		kubernetesLabels   kubernetesConfig
		stackdriverAddress *url.URL
		stackdriverDebug   bool
		walDirectory       string
		prometheusURL      *url.URL
		listenAddress      string
		filters            []string

		logLevel promlog.AllowedLevel
	}{}

	a := kingpin.New(filepath.Base(os.Args[0]), "The Prometheus monitoring server")

	a.Version(version.Print("prometheus"))

	a.HelpFlag.Short('h')

	projectId := a.Flag("stackdriver.project-id", "The Google project ID where Stackdriver will store the metrics.").
		Required().
		String()

	a.Flag("stackdriver.api-address", "Address of the Stackdriver Monitoring API.").
		Default("https://monitoring.googleapis.com:443/").URLVar(&cfg.stackdriverAddress)

	a.Flag("stackdriver.kubernetes.location", "Value of the 'location' label in the Kubernetes Stackdriver MonitoredResources.").
		StringVar(&cfg.kubernetesLabels.location)

	a.Flag("stackdriver.kubernetes.cluster-name", "Value of the 'cluster_name' label in the Kubernetes Stackdriver MonitoredResources.").
		StringVar(&cfg.kubernetesLabels.clusterName)

	a.Flag("stackdriver.debug", "Use the debug monitored resource to send data to a fake receiver.").
		BoolVar(&cfg.stackdriverDebug)

	a.Flag("prometheus.wal-directory", "Directory from where to read the Prometheus TSDB WAL.").
		Default("data/wal").StringVar(&cfg.walDirectory)

	a.Flag("prometheus.api-address", "Address to listen on for UI, API, and telemetry.").
		Default("http://127.0.0.1:9090/").URLVar(&cfg.prometheusURL)

	a.Flag("web.listen-address", "Address to listen on for UI, API, and telemetry.").
		Default("0.0.0.0:9091").StringVar(&cfg.listenAddress)

	a.Flag("filter", "PromQL-style label matcher which must pass for a series to be forwarded to Stackdriver. May be repeated.").
		StringsVar(&cfg.filters)

	promlogflag.AddFlags(a, &cfg.logLevel)

	_, err := a.Parse(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, errors.Wrapf(err, "Error parsing commandline arguments"))
		a.Usage(os.Args[1:])
		os.Exit(2)
	}

	logger := promlog.New(cfg.logLevel)

	level.Info(logger).Log("msg", "Starting Stackdriver Prometheus sidecar", "version", version.Info())
	level.Info(logger).Log("build_context", version.BuildContext())
	level.Info(logger).Log("host_details", Uname())
	level.Info(logger).Log("fd_limits", FdLimits())

	promExporter, err := oc_prometheus.NewExporter(oc_prometheus.Options{
		Registry: prometheus.DefaultRegisterer.(*prometheus.Registry),
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "creating Prometheus exporter failed:", err)
		os.Exit(1)
	}
	view.RegisterExporter(promExporter)

	httpClient := &http.Client{Transport: &ochttp.Transport{}}

	var staticLabels = map[string]string{
		retrieval.ProjectIDLabel:             *projectId,
		retrieval.KubernetesLocationLabel:    cfg.kubernetesLabels.location,
		retrieval.KubernetesClusterNameLabel: cfg.kubernetesLabels.clusterName,
	}
	if cfg.stackdriverDebug {
		staticLabels["_debug"] = "debug"
	}

	filters, err := parseFilters(cfg.filters...)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Error parsing filters:", err)
		os.Exit(2)
	}

	cfg.projectIdResource = fmt.Sprintf("projects/%v", *projectId)
	targetsURL, err := cfg.prometheusURL.Parse(targets.DefaultAPIEndpoint)
	if err != nil {
		panic(err)
	}
	targetCache := targets.NewCache(logger, httpClient, targetsURL)

	metadataURL, err := cfg.prometheusURL.Parse(metadata.DefaultEndpointPath)
	if err != nil {
		panic(err)
	}
	metadataCache := metadata.NewCache(httpClient, metadataURL)

	// We instantiate a context here since the tailer is used by two other components.
	// The context will be used in the lifecycle of prometheusReader further down.
	ctx, cancel := context.WithCancel(context.Background())

	tailer, err := tail.Tail(ctx, cfg.walDirectory)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Tailing WAL failed:", err)
		os.Exit(1)
	}
	// TODO(jkohen): Remove once we have proper translation of all metric
	// types. Currently Stackdriver fails the entire request if you attempt
	// to write to the different metric type, which we do fairly often at
	// this point, so lots of writes fail, and most writes fail.
	// config.DefaultQueueConfig.MaxSamplesPerSend = 1
	config.DefaultQueueConfig.MaxSamplesPerSend = stackdriver.MaxTimeseriesesPerRequest
	// We want the queues to have enough buffer to ensure consistent flow with full batches
	// being available for every new request.
	// Testing with different latencies and shard numbers have shown that 3x of the batch size
	// works well.
	config.DefaultQueueConfig.Capacity = 3 * stackdriver.MaxTimeseriesesPerRequest

	queueManager, err := stackdriver.NewQueueManager(
		log.With(logger, "component", "queue_manager"),
		config.DefaultQueueConfig,
		&clientFactory{
			logger:            log.With(logger, "component", "storage"),
			projectIdResource: cfg.projectIdResource,
			url:               cfg.stackdriverAddress,
			timeout:           10 * time.Second,
		},
		tailer,
	)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Creating queue manager failed:", err)
		os.Exit(1)
	}
	prometheusReader := retrieval.NewPrometheusReader(
		log.With(logger, "component", "Prometheus reader"),
		cfg.walDirectory,
		tailer,
		filters,
		retrieval.TargetsWithDiscoveredLabels(targetCache, labels.FromMap(staticLabels)),
		metadataCache,
		queueManager,
	)

	// Exclude kingpin default flags to expose only Prometheus ones.
	boilerplateFlags := kingpin.New("", "").Version("")
	for _, f := range a.Model().Flags {
		if boilerplateFlags.GetFlag(f.Name) != nil {
			continue
		}

	}

	// Monitor outgoing connections on default transport with conntrack.
	http.DefaultTransport.(*http.Transport).DialContext = conntrack.NewDialContextFunc(
		conntrack.DialWithTracing(),
	)

	http.Handle("/metrics", promhttp.Handler())

	var g group.Group
	{
		ctx, cancel := context.WithCancel(context.Background())
		g.Add(func() error {
			targetCache.Run(ctx)
			return nil
		}, func(error) {
			cancel()
		})
	}
	{
		term := make(chan os.Signal)
		signal.Notify(term, os.Interrupt, syscall.SIGTERM)
		cancel := make(chan struct{})
		g.Add(
			func() error {
				// Don't forget to release the reloadReady channel so that waiting blocks can exit normally.
				select {
				case <-term:
					level.Warn(logger).Log("msg", "Received SIGTERM, exiting gracefully...")
				case <-cancel:
					break
				}
				return nil
			},
			func(err error) {
				close(cancel)
			},
		)
	}
	{
		// We use the context we defined higher up instead of a local one like in the other actors.
		// This is necessary since it's also used to manage the tailer's lifecycle, which the reader
		// depends on to exit properly.
		g.Add(
			func() error {
				startOffset, err := retrieval.ReadProgressFile(cfg.walDirectory)
				if err != nil {
					level.Warn(logger).Log("msg", "reading progress file failed", "err", err)
					startOffset = 0
				}
				// Write the file again once to ensure we have write permission on startup.
				if err := retrieval.SaveProgressFile(cfg.walDirectory, startOffset); err != nil {
					return err
				}
				waitForPrometheus(ctx, logger, cfg.prometheusURL)
				// Sleep a fixed amount of time to allow the first scrapes to complete.
				select {
				case <-time.After(time.Minute):
				case <-ctx.Done():
					return nil
				}
				err = prometheusReader.Run(ctx, startOffset)
				level.Info(logger).Log("msg", "Prometheus reader stopped")
				return err
			},
			func(err error) {
				// Prometheus reader needs to be stopped before closing the TSDB
				// so that it doesn't try to write samples to a closed storage.
				level.Info(logger).Log("msg", "Stopping Prometheus reader...")
				cancel()
			},
		)
	}
	{
		cancel := make(chan struct{})
		g.Add(
			func() error {
				if err := queueManager.Start(); err != nil {
					return err
				}
				level.Info(logger).Log("msg", "Stackdriver client started")
				<-cancel
				return nil
			},
			func(err error) {
				if err := queueManager.Stop(); err != nil {
					level.Error(logger).Log("msg", "Error stopping Stackdriver writer", "err", err)
				}
				close(cancel)
			},
		)
	}
	{
		cancel := make(chan struct{})
		server := &http.Server{
			Addr: cfg.listenAddress,
		}
		g.Add(
			func() error {
				level.Info(logger).Log("msg", "Web server started")
				err := server.ListenAndServe()
				if err != http.ErrServerClosed {
					return err
				}
				<-cancel
				return nil
			},
			func(err error) {
				if err := server.Shutdown(context.Background()); err != nil {
					level.Error(logger).Log("msg", "Error stopping web server", "err", err)
				}
				close(cancel)
			},
		)
	}
	if err := g.Run(); err != nil {
		level.Error(logger).Log("err", err)
	}
	level.Info(logger).Log("msg", "See you next time!")
}

type clientFactory struct {
	logger            log.Logger
	projectIdResource string
	url               *url.URL
	timeout           time.Duration
}

func (f *clientFactory) New() stackdriver.StorageClient {
	return stackdriver.NewClient(&stackdriver.ClientConfig{
		Logger:    f.logger,
		ProjectId: f.projectIdResource,
		URL:       f.url,
		Timeout:   f.timeout,
	})
}

func (f *clientFactory) Name() string {
	return f.url.String()
}

func waitForPrometheus(ctx context.Context, logger log.Logger, promURL *url.URL) {
	tick := time.NewTicker(3 * time.Second)
	defer tick.Stop()

	u := *promURL
	u.Path = path.Join(promURL.Path, "/-/ready")

	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			resp, err := http.Get(u.String())
			if err != nil {
				level.Warn(logger).Log("msg", "query Prometheus readiness", "err", err)
				continue
			}
			if resp.StatusCode/100 == 2 {
				return
			}
			level.Warn(logger).Log("msg", "Prometheus not ready", "status", resp.Status)
		}
	}
}

// parseFilters parses a list of strings that contain PromQL-style label matchers and
// returns a list of the resulting matchers.
func parseFilters(strs ...string) (matchers []*labels.Matcher, err error) {
	pattern := regexp.MustCompile(`^([a-zA-Z0-9_]+)(=|!=|=~|!~)"(.+)"$`)

	for _, s := range strs {
		parts := pattern.FindStringSubmatch(s)
		if len(parts) != 4 {
			return nil, fmt.Errorf("invalid filter %q", s)
		}
		var matcherType labels.MatchType
		switch parts[2] {
		case "=":
			matcherType = labels.MatchEqual
		case "!=":
			matcherType = labels.MatchNotEqual
		case "=~":
			matcherType = labels.MatchRegexp
		case "!~":
			matcherType = labels.MatchNotRegexp
		}
		matcher, err := labels.NewMatcher(matcherType, parts[1], parts[3])
		if err != nil {
			return nil, fmt.Errorf("invalid filter %q: %s", s, err)
		}
		matchers = append(matchers, matcher)
	}
	return matchers, nil
}
