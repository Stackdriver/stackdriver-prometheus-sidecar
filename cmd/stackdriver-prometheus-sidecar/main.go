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
	"io/ioutil"
	"net/http"
	_ "net/http/pprof" // Comment this line to disable pprof endpoint.
	"net/url"
	"os"
	"os/signal"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	md "cloud.google.com/go/compute/metadata"
	oc_prometheus "contrib.go.opencensus.io/exporter/prometheus"
	oc_stackdriver "contrib.go.opencensus.io/exporter/stackdriver"
	"github.com/Stackdriver/stackdriver-prometheus-sidecar/metadata"
	"github.com/Stackdriver/stackdriver-prometheus-sidecar/retrieval"
	"github.com/Stackdriver/stackdriver-prometheus-sidecar/stackdriver"
	"github.com/Stackdriver/stackdriver-prometheus-sidecar/tail"
	"github.com/Stackdriver/stackdriver-prometheus-sidecar/targets"
	"github.com/ghodss/yaml"
	"github.com/go-kit/kit/log"
	"github.com/go-kit/kit/log/level"
	conntrack "github.com/mwitkow/go-conntrack"
	"github.com/oklog/oklog/pkg/group"
	"github.com/pkg/errors"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/prometheus/common/promlog"
	promlogflag "github.com/prometheus/common/promlog/flag"
	"github.com/prometheus/common/version"
	"github.com/prometheus/prometheus/config"
	"github.com/prometheus/prometheus/pkg/labels"
	"github.com/prometheus/prometheus/pkg/textparse"
	"github.com/prometheus/prometheus/promql"
	"go.opencensus.io/plugin/ocgrpc"
	"go.opencensus.io/plugin/ochttp"
	"go.opencensus.io/resource"
	"go.opencensus.io/stats"
	"go.opencensus.io/stats/view"
	"go.opencensus.io/tag"
	metric_pb "google.golang.org/genproto/googleapis/api/metric"
	"google.golang.org/grpc/resolver"
	"google.golang.org/grpc/resolver/manual"
	kingpin "gopkg.in/alecthomas/kingpin.v2"
)

var (
	sizeDistribution    = view.Distribution(0, 1024, 2048, 4096, 16384, 65536, 262144, 1048576, 4194304, 33554432)
	latencyDistribution = view.Distribution(0, 1, 2, 5, 10, 15, 25, 50, 100, 200, 400, 800, 1500, 3000, 6000)

	// VersionTag identifies the version of this binary.
	VersionTag = tag.MustNewKey("version")
	// UptimeMeasure is a cumulative metric.
	UptimeMeasure = stats.Int64(
		"agent.googleapis.com/agent/uptime",
		"uptime of the Stackdriver Prometheus collector",
		stats.UnitSeconds)
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
		ocgrpc.DefaultClientViews...,
	); err != nil {
		panic(err)
	}
	if err := view.Register(
		&view.View{
			Measure:     UptimeMeasure,
			TagKeys:     []tag.Key{VersionTag},
			Aggregation: view.Sum(),
		},
	); err != nil {
		panic(err)
	}
}

type kubernetesConfig struct {
	Location    string
	ClusterName string
}

type genericConfig struct {
	Location  string
	Namespace string
}

type metricRenamesConfig struct {
	From string `json:"from"`
	To   string `json:"to"`
}

type staticMetadataConfig struct {
	Metric    string `json:"metric"`
	Type      string `json:"type"`
	ValueType string `json:"value_type"`
	Help      string `json:"help"`
}

type aggregatedCountersConfig struct {
	Metric  string   `json:"metric"`
	Filters []string `json:"filters"`
	Help    string   `json:"help"`
}

type fileConfig struct {
	MetricRenames      []metricRenamesConfig      `json:"metric_renames"`
	StaticMetadata     []staticMetadataConfig     `json:"static_metadata"`
	AggregatedCounters []aggregatedCountersConfig `json:"aggregated_counters"`
}

// Note: When adding a new config field, consider adding it to
// statusz-tmpl.html
type mainConfig struct {
	ConfigFilename        string
	ProjectIDResource     string
	KubernetesLabels      kubernetesConfig
	GenericLabels         genericConfig
	StackdriverAddress    *url.URL
	MetricsPrefix         string
	UseGKEResource        bool
	StoreInFilesDirectory string
	WALDirectory          string
	PrometheusURL         *url.URL
	ListenAddress         string
	EnableStatusz         bool
	Filters               []string
	Filtersets            []string
	Aggregations          retrieval.CounterAggregatorConfig
	MetricRenames         map[string]string
	StaticMetadata        []*metadata.Entry
	UseRestrictedIPs      bool
	manualResolver        *manual.Resolver
	MonitoringBackends    []string
	PromlogConfig         promlog.Config
}

func main() {
	if os.Getenv("DEBUG") != "" {
		runtime.SetBlockProfileRate(20)
		runtime.SetMutexProfileFraction(20)
	}

	var cfg mainConfig

	a := kingpin.New(filepath.Base(os.Args[0]), "The Prometheus monitoring server")

	a.Version(version.Print("prometheus"))

	a.HelpFlag.Short('h')

	a.Flag("config-file", "A configuration file.").StringVar(&cfg.ConfigFilename)

	projectID := a.Flag("stackdriver.project-id", "The Google project ID where Stackdriver will store the metrics.").
		Required().
		String()

	a.Flag("stackdriver.api-address", "Address of the Stackdriver Monitoring API.").
		Default("https://monitoring.googleapis.com:443/").URLVar(&cfg.StackdriverAddress)

	a.Flag("stackdriver.use-restricted-ips", "If true, send all requests through restricted VIPs (EXPERIMENTAL).").
		Default("false").BoolVar(&cfg.UseRestrictedIPs)

	a.Flag("stackdriver.kubernetes.location", "Value of the 'location' label in the Kubernetes Stackdriver MonitoredResources.").
		StringVar(&cfg.KubernetesLabels.Location)

	a.Flag("stackdriver.kubernetes.cluster-name", "Value of the 'cluster_name' label in the Kubernetes Stackdriver MonitoredResources.").
		StringVar(&cfg.KubernetesLabels.ClusterName)

	a.Flag("stackdriver.generic.location", "Location for metrics written with the generic resource, e.g. a cluster or data center name.").
		StringVar(&cfg.GenericLabels.Location)

	a.Flag("stackdriver.generic.namespace", "Namespace for metrics written with the generic resource, e.g. a cluster or data center name.").
		StringVar(&cfg.GenericLabels.Namespace)

	a.Flag("stackdriver.metrics-prefix", "Customized prefix for Stackdriver metrics. If not set, external.googleapis.com/prometheus will be used").
		StringVar(&cfg.MetricsPrefix)

	a.Flag("stackdriver.use-gke-resource",
		"Whether to use the legacy gke_container MonitoredResource type instead of k8s_container").
		Default("false").BoolVar(&cfg.UseGKEResource)

	a.Flag("stackdriver.store-in-files-directory", "If specified, store the CreateTimeSeriesRequest protobuf messages to files under this directory, instead of sending protobuf messages to Stackdriver Monitoring API.").
		StringVar(&cfg.StoreInFilesDirectory)

	a.Flag("prometheus.wal-directory", "Directory from where to read the Prometheus TSDB WAL.").
		Default("data/wal").StringVar(&cfg.WALDirectory)

	a.Flag("prometheus.api-address", "Address to listen on for UI, API, and telemetry.").
		Default("http://127.0.0.1:9090/").URLVar(&cfg.PrometheusURL)

	a.Flag("monitoring.backend", "Monitoring backend(s) for internal metrics").Default("prometheus").
		EnumsVar(&cfg.MonitoringBackends, "prometheus", "stackdriver")

	a.Flag("web.listen-address", "Address to listen on for UI, API, and telemetry.").
		Default("0.0.0.0:9091").StringVar(&cfg.ListenAddress)

	a.Flag("web.enable-statusz", "If true, then enables a /statusz endpoint on the web server with diagnostic information.").
		Default("true").BoolVar(&cfg.EnableStatusz)

	a.Flag("include", "PromQL metric and label matcher which must pass for a series to be forwarded to Stackdriver. If repeated, the series must pass any of the filter sets to be forwarded.").
		StringsVar(&cfg.Filtersets)

	a.Flag("filter", "PromQL-style matcher for a single label which must pass for a series to be forwarded to Stackdriver. If repeated, the series must pass all filters to be forwarded. Deprecated, please use --include instead.").
		StringsVar(&cfg.Filters)

	promlogflag.AddFlags(a, &cfg.PromlogConfig)

	_, err := a.Parse(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, errors.Wrapf(err, "Error parsing commandline arguments"))
		a.Usage(os.Args[1:])
		os.Exit(2)
	}

	logger := promlog.New(&cfg.PromlogConfig)
	if cfg.ConfigFilename != "" {
		cfg.MetricRenames, cfg.StaticMetadata, cfg.Aggregations, err = parseConfigFile(cfg.ConfigFilename)
		if err != nil {
			msg := fmt.Sprintf("Parse config file %s", cfg.ConfigFilename)
			level.Error(logger).Log("msg", msg, "err", err)
			os.Exit(2)
		}

		// Enable Stackdriver monitoring backend if counter aggregator configuration is present.
		if len(cfg.Aggregations) > 0 {
			sdEnabled := false
			for _, backend := range cfg.MonitoringBackends {
				if backend == "stackdriver" {
					sdEnabled = true
				}
			}
			if !sdEnabled {
				cfg.MonitoringBackends = append(cfg.MonitoringBackends, "stackdriver")
			}
		}
	}

	level.Info(logger).Log("msg", "Starting Stackdriver Prometheus sidecar", "version", version.Info())
	level.Info(logger).Log("build_context", version.BuildContext())
	level.Info(logger).Log("host_details", Uname())
	level.Info(logger).Log("fd_limits", FdLimits())

	// We instantiate a context here since the tailer is used by two other components.
	// The context will be used in the lifecycle of prometheusReader further down.
	ctx, cancel := context.WithCancel(context.Background())

	go func() {
		uptimeUpdateTime := time.Now()
		c := time.Tick(60 * time.Second)
		for now := range c {
			stats.RecordWithTags(ctx,
				[]tag.Mutator{tag.Upsert(VersionTag, fmt.Sprintf("stackdriver-prometheus-sidecar/%s", version.Version))},
				UptimeMeasure.M(int64(now.Sub(uptimeUpdateTime).Seconds())))
			uptimeUpdateTime = now
		}
	}()

	httpClient := &http.Client{Transport: &ochttp.Transport{}}

	if *projectID == "" {
		*projectID = getGCEProjectID()
	}

	for _, backend := range cfg.MonitoringBackends {
		switch backend {
		case "prometheus":
			promExporter, err := oc_prometheus.NewExporter(oc_prometheus.Options{
				Registry: prometheus.DefaultRegisterer.(*prometheus.Registry),
			})
			if err != nil {
				level.Error(logger).Log("msg", "Creating Prometheus exporter failed", "err", err)
				os.Exit(1)
			}
			view.RegisterExporter(promExporter)
		case "stackdriver":
			const reportingInterval = 60 * time.Second
			sd, err := oc_stackdriver.NewExporter(oc_stackdriver.Options{
				ProjectID: *projectID,
				// If the OpenCensus resource environment variables aren't set, the monitored resource will likely fall back to `generic_task`.
				ResourceDetector:  resource.FromEnv,
				ReportingInterval: reportingInterval,
				// Disable default `opencensus_task` label.
				DefaultMonitoringLabels: &oc_stackdriver.Labels{},
				GetMetricType: func(v *view.View) string {
					// Curated metrics produced by this process.
					if strings.Contains(v.Name, "agent.googleapis.com") {
						return v.Name
					}
					// Default OpenCensus behavior.
					return path.Join("custom.googleapis.com", "opencensus", v.Name)
				},
			})
			if err != nil {
				level.Error(logger).Log("msg", "Creating Stackdriver exporter failed", "err", err)
				os.Exit(1)
			}
			defer sd.Flush()
			view.RegisterExporter(sd)
			view.SetReportingPeriod(reportingInterval)
		default:
			level.Error(logger).Log("msg", "Unknown monitoring backend", "backend", backend)
			os.Exit(1)
		}
	}

	var staticLabels = map[string]string{
		retrieval.ProjectIDLabel:             *projectID,
		retrieval.KubernetesLocationLabel:    cfg.KubernetesLabels.Location,
		retrieval.KubernetesClusterNameLabel: cfg.KubernetesLabels.ClusterName,
		retrieval.GenericLocationLabel:       cfg.GenericLabels.Location,
		retrieval.GenericNamespaceLabel:      cfg.GenericLabels.Namespace,
	}
	fillMetadata(&staticLabels)
	for k, v := range staticLabels {
		if v == "" {
			delete(staticLabels, k)
		}
	}

	filtersets, err := parseFiltersets(logger, cfg.Filtersets, cfg.Filters)
	if err != nil {
		level.Error(logger).Log("msg", "Error parsing --include (or --filter)", "err", err)
		os.Exit(2)
	}

	cfg.ProjectIDResource = fmt.Sprintf("projects/%v", *projectID)
	if cfg.UseRestrictedIPs {
		// manual.GenerateAndRegisterManualResolver generates a Resolver and a random scheme.
		// It also registers the resolver. rb.InitialAddrs adds the addresses we are using
		// to resolve GCP API calls to the resolver.
		cfg.manualResolver, _ = manual.GenerateAndRegisterManualResolver()
		// These IP addresses correspond to restricted.googleapis.com and are not expected to change.
		cfg.manualResolver.InitialState(resolver.State{
			Addresses: []resolver.Address{
				{Addr: "199.36.153.4:443"},
				{Addr: "199.36.153.5:443"},
				{Addr: "199.36.153.6:443"},
				{Addr: "199.36.153.7:443"},
			},
		})
	}
	targetsURL, err := cfg.PrometheusURL.Parse(targets.DefaultAPIEndpoint)
	if err != nil {
		panic(err)
	}
	targetCache := targets.NewCache(logger, httpClient, targetsURL)

	metadataURL, err := cfg.PrometheusURL.Parse(metadata.DefaultEndpointPath)
	if err != nil {
		panic(err)
	}
	metadataCache := metadata.NewCache(httpClient, metadataURL, cfg.StaticMetadata)

	tailer, err := tail.Tail(ctx, cfg.WALDirectory)
	if err != nil {
		level.Error(logger).Log("msg", "Tailing WAL failed", "err", err)
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

	var scf stackdriver.StorageClientFactory

	if len(cfg.StoreInFilesDirectory) > 0 {
		err := os.MkdirAll(cfg.StoreInFilesDirectory, 0700)
		if err != nil {
			level.Error(logger).Log(
				"msg", "Failure creating directory.",
				"err", err)
			os.Exit(1)
		}
		scf = &fileClientFactory{
			dir:    cfg.StoreInFilesDirectory,
			logger: log.With(logger, "component", "storage"),
		}
	} else {
		scf = &stackdriverClientFactory{
			logger:            log.With(logger, "component", "storage"),
			projectIDResource: cfg.ProjectIDResource,
			url:               cfg.StackdriverAddress,
			timeout:           10 * time.Second,
			manualResolver:    cfg.manualResolver,
		}
	}

	queueManager, err := stackdriver.NewQueueManager(
		log.With(logger, "component", "queue_manager"),
		config.DefaultQueueConfig,
		scf,
		tailer,
	)
	if err != nil {
		level.Error(logger).Log("msg", "Creating queue manager failed", "err", err)
		os.Exit(1)
	}

	counterAggregator, err := retrieval.NewCounterAggregator(
		log.With(logger, "component", "counter_aggregator"),
		&cfg.Aggregations)
	if err != nil {
		level.Error(logger).Log("msg", "Creating counter aggregator failed", "err", err)
		os.Exit(1)
	}
	defer counterAggregator.Close()

	prometheusReader := retrieval.NewPrometheusReader(
		log.With(logger, "component", "Prometheus reader"),
		cfg.WALDirectory,
		tailer,
		filtersets,
		cfg.MetricRenames,
		retrieval.TargetsWithDiscoveredLabels(targetCache, labels.FromMap(staticLabels)),
		metadataCache,
		queueManager,
		cfg.MetricsPrefix,
		cfg.UseGKEResource,
		counterAggregator,
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

	if cfg.EnableStatusz {
		http.Handle("/statusz", &statuszHandler{
			logger:    logger,
			projectID: *projectID,
			cfg:       &cfg,
		})
	}

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
				startOffset, err := retrieval.ReadProgressFile(cfg.WALDirectory)
				if err != nil {
					level.Warn(logger).Log("msg", "reading progress file failed", "err", err)
					startOffset = 0
				}
				// Write the file again once to ensure we have write permission on startup.
				if err := retrieval.SaveProgressFile(cfg.WALDirectory, startOffset); err != nil {
					return err
				}
				waitForPrometheus(ctx, logger, cfg.PrometheusURL)
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
			Addr: cfg.ListenAddress,
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

type stackdriverClientFactory struct {
	logger            log.Logger
	projectIDResource string
	url               *url.URL
	timeout           time.Duration
	manualResolver    *manual.Resolver
}

func (s *stackdriverClientFactory) New() stackdriver.StorageClient {
	return stackdriver.NewClient(&stackdriver.ClientConfig{
		Logger:    s.logger,
		ProjectID: s.projectIDResource,
		URL:       s.url,
		Timeout:   s.timeout,
		Resolver:  s.manualResolver,
	})
}

func (s *stackdriverClientFactory) Name() string {
	return s.url.String()
}

// fileClientFactory generates StorageClient which writes to a newly
// created file under dir. It requires dir an existing valid directory.
type fileClientFactory struct {
	dir    string
	logger log.Logger
}

// New creates an instance of stackdriver.StorageClient. Each instance
// writes to a different file under dir. The returned instance is not
// thread-safe.
func (fcf *fileClientFactory) New() stackdriver.StorageClient {
	f, err := ioutil.TempFile(fcf.dir, "*.txt")
	if err != nil {
		level.Error(fcf.logger).Log(
			"msg", "failure creating files.",
			"err", err)
	}
	return stackdriver.NewCreateTimeSeriesRequestWriterCloser(f, fcf.logger)
}

func (fcf *fileClientFactory) Name() string {
	return "fileClientFactory"
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

// parseFiltersets parses two flags that contain PromQL-style metric/label selectors and
// returns a list of the resulting matchers.
func parseFiltersets(logger log.Logger, filtersets, filters []string) ([][]*labels.Matcher, error) {
	var matchers [][]*labels.Matcher
	if len(filters) > 0 {
		level.Warn(logger).Log("msg", "--filter is deprecated; please use --include instead")
		f := fmt.Sprintf("{%s}", strings.Join(filters, ","))
		m, err := promql.ParseMetricSelector(f)
		if err != nil {
			return nil, errors.Errorf("cannot parse --filter flag (metric filter '%s'): %q", f, err)
		}
		matchers = append(matchers, m)
	}
	for _, f := range filtersets {
		m, err := promql.ParseMetricSelector(f)
		if err != nil {
			return nil, errors.Errorf("cannot parse --include flag '%s': %q", f, err)
		}
		matchers = append(matchers, m)
	}
	return matchers, nil
}

func getGCEProjectID() string {
	if !md.OnGCE() {
		return ""
	}
	if id, err := md.ProjectID(); err == nil {
		return strings.TrimSpace(id)
	}
	return ""
}

func fillMetadata(staticConfig *map[string]string) {
	if !md.OnGCE() {
		return
	}
	if (*staticConfig)[retrieval.ProjectIDLabel] == "" {
		if id, err := md.ProjectID(); err == nil {
			id = strings.TrimSpace(id)
			(*staticConfig)[retrieval.ProjectIDLabel] = id
		}
	}
	if (*staticConfig)[retrieval.KubernetesLocationLabel] == "" {
		if l, err := md.InstanceAttributeValue("cluster-location"); err == nil {
			l = strings.TrimSpace(l)
			(*staticConfig)[retrieval.KubernetesLocationLabel] = l
		}
	}
	if (*staticConfig)[retrieval.KubernetesClusterNameLabel] == "" {
		if cn, err := md.InstanceAttributeValue("cluster-name"); err == nil {
			cn = strings.TrimSpace(cn)
			(*staticConfig)[retrieval.KubernetesClusterNameLabel] = cn
		}
	}
}

func parseConfigFile(filename string) (map[string]string, []*metadata.Entry, retrieval.CounterAggregatorConfig, error) {
	b, err := ioutil.ReadFile(filename)
	if err != nil {
		return nil, nil, nil, errors.Wrap(err, "reading file")
	}
	var fc fileConfig
	if err := yaml.Unmarshal(b, &fc); err != nil {
		return nil, nil, nil, errors.Wrap(err, "invalid YAML")
	}
	return processFileConfig(fc)
}

func processFileConfig(fc fileConfig) (map[string]string, []*metadata.Entry, retrieval.CounterAggregatorConfig, error) {
	renameMapping := map[string]string{}
	for _, r := range fc.MetricRenames {
		renameMapping[r.From] = r.To
	}
	staticMetadata := []*metadata.Entry{}
	for _, sm := range fc.StaticMetadata {
		switch sm.Type {
		case metadata.MetricTypeUntyped:
			// Convert "untyped" to the "unknown" type used internally as of Prometheus 2.5.
			sm.Type = textparse.MetricTypeUnknown
		case textparse.MetricTypeCounter, textparse.MetricTypeGauge, textparse.MetricTypeHistogram,
			textparse.MetricTypeSummary, textparse.MetricTypeUnknown:
		default:
			return nil, nil, nil, errors.Errorf("invalid metric type %q", sm.Type)
		}
		var valueType metric_pb.MetricDescriptor_ValueType
		switch sm.ValueType {
		case "double":
			valueType = metric_pb.MetricDescriptor_DOUBLE
		case "int64":
			valueType = metric_pb.MetricDescriptor_INT64
		case "":
			valueType = metric_pb.MetricDescriptor_VALUE_TYPE_UNSPECIFIED
		default:
			return nil, nil, nil, errors.Errorf("invalid value type %q", sm.ValueType)
		}
		staticMetadata = append(staticMetadata,
			&metadata.Entry{Metric: sm.Metric, MetricType: textparse.MetricType(sm.Type), ValueType: valueType, Help: sm.Help})
	}

	aggregations := make(retrieval.CounterAggregatorConfig)
	for _, c := range fc.AggregatedCounters {
		if _, ok := aggregations[c.Metric]; ok {
			return nil, nil, nil, errors.Errorf("duplicate counter aggregator metric %s", c.Metric)
		}
		a := &retrieval.CounterAggregatorMetricConfig{Help: c.Help}
		for _, f := range c.Filters {
			matcher, err := promql.ParseMetricSelector(f)
			if err != nil {
				return nil, nil, nil, errors.Errorf("cannot parse metric selector '%s': %q", f, err)
			}
			a.Matchers = append(a.Matchers, matcher)
		}
		aggregations[c.Metric] = a
	}
	return renameMapping, staticMetadata, aggregations, nil
}
