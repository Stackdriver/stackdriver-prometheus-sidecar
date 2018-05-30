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
	"net"
	"net/http"
	_ "net/http/pprof" // Comment this line to disable pprof endpoint.
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
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
	k8s_runtime "k8s.io/apimachinery/pkg/util/runtime"

	"github.com/Stackdriver/stackdriver-prometheus-sidecar/retrieval"
	"github.com/Stackdriver/stackdriver-prometheus-sidecar/stackdriver"
	"github.com/prometheus/common/promlog"
	promlogflag "github.com/prometheus/common/promlog/flag"
	"github.com/prometheus/prometheus/config"
)

var (
	configSuccess = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: "prometheus",
		Name:      "config_last_reload_successful",
		Help:      "Whether the last configuration reload attempt was successful.",
	})
	configSuccessTime = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: "prometheus",
		Name:      "config_last_reload_success_timestamp_seconds",
		Help:      "Timestamp of the last successful configuration reload.",
	})
)

func init() {
	prometheus.MustRegister(version.NewCollector("prometheus"))
}

func main() {
	if os.Getenv("DEBUG") != "" {
		runtime.SetBlockProfileRate(20)
		runtime.SetMutexProfileFraction(20)
	}

	cfg := struct {
		configFile string

		walDirectory      string
		prometheusAddress string
		listenAddress     string
		sdCfg             stackdriver.StackdriverConfig

		logLevel promlog.AllowedLevel
	}{
		sdCfg: stackdriver.DefaultStackdriverConfig,
	}

	a := kingpin.New(filepath.Base(os.Args[0]), "The Prometheus monitoring server")

	a.Version(version.Print("prometheus"))

	a.HelpFlag.Short('h')

	a.Flag("config.file", "Configuration file path.").
		Default("stackdriver-prometheus.yml").StringVar(&cfg.configFile)

	a.Flag("prometheus.wal-directory", "Directory from where to read the Prometheus TSDB WAL.").
		Default("data/wal").StringVar(&cfg.walDirectory)

	a.Flag("prometheus.api-address", "Address to listen on for UI, API, and telemetry.").
		Default("0.0.0.0:9090").StringVar(&cfg.prometheusAddress)

	a.Flag("web.listen-address", "Address to listen on for UI, API, and telemetry.").
		Default("0.0.0.0:9091").StringVar(&cfg.listenAddress)

	promlogflag.AddFlags(a, &cfg.logLevel)

	_, err := a.Parse(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, errors.Wrapf(err, "Error parsing commandline arguments"))
		a.Usage(os.Args[1:])
		os.Exit(2)
	}

	logger := promlog.New(cfg.logLevel)

	// XXX(fabxc): Kubernetes does background logging which we can only customize by modifying
	// a global variable.
	// Ultimately, here is the best place to set it.
	k8s_runtime.ErrorHandlers = []func(error){
		func(err error) {
			level.Error(log.With(logger, "component", "k8s_client_runtime")).Log("err", err)
		},
	}

	level.Info(logger).Log("msg", "Starting Stackdriver Prometheus sidecar", "version", version.Info())
	level.Info(logger).Log("build_context", version.BuildContext())
	level.Info(logger).Log("host_details", Uname())
	level.Info(logger).Log("fd_limits", FdLimits())

	var (
		remoteStorage    = stackdriver.NewStorage(log.With(logger, "component", "remote"), &cfg.sdCfg)
		prometheusReader = retrieval.NewPrometheusReader(log.With(logger, "component", "Prometheus reader"), cfg.walDirectory, remoteStorage)
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

	// TODO(jkohen): replace with a config file specific to the sidecar.
	reloaders := []func(cfg *config.Config) error{
		remoteStorage.ApplyConfig,
		prometheusReader.ApplyConfig,
	}

	prometheus.MustRegister(configSuccess)
	prometheus.MustRegister(configSuccessTime)

	http.Handle("/metrics", promhttp.Handler())

	// sync.Once is used to make sure we can close the channel at different execution stages(SIGTERM or when the config is loaded).
	type closeOnce struct {
		C     chan struct{}
		once  sync.Once
		Close func()
	}
	// Wait until the server is ready to handle reloading.
	reloadReady := &closeOnce{
		C: make(chan struct{}),
	}
	reloadReady.Close = func() {
		reloadReady.once.Do(func() {
			close(reloadReady.C)
		})
	}

	var g group.Group
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
					reloadReady.Close()
				case <-cancel:
					reloadReady.Close()
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
		g.Add(
			func() error {
				// When the Prometheus reader receives a new targets list
				// it needs to read a valid config for each job.
				select {
				case <-reloadReady.C:
					break
				}

				err := prometheusReader.Run()
				level.Info(logger).Log("msg", "Prometheus reader stopped")
				return err
			},
			func(err error) {
				// Prometheus reader needs to be stopped before closing the TSDB
				// so that it doesn't try to write samples to a closed storage.
				level.Info(logger).Log("msg", "Stopping Prometheus reader...")
				prometheusReader.Stop()
			},
		)
	}
	{
		// Make sure that sighup handler is registered with a redirect to the channel before the potentially
		// long and synchronous tsdb init.
		hup := make(chan os.Signal)
		signal.Notify(hup, syscall.SIGHUP)
		cancel := make(chan struct{})
		g.Add(
			func() error {
				<-reloadReady.C

				for {
					select {
					case <-hup:
						if err := reloadConfig(cfg.configFile, logger, reloaders...); err != nil {
							level.Error(logger).Log("msg", "Error reloading config", "err", err)
						}
					case <-cancel:
						return nil
					}
				}

			},
			func(err error) {
				close(cancel)
			},
		)
	}
	{
		cancel := make(chan struct{})
		g.Add(
			func() error {
				if err := reloadConfig(cfg.configFile, logger, reloaders...); err != nil {
					return fmt.Errorf("Error loading config %s", err)
				}

				reloadReady.Close()
				level.Info(logger).Log("msg", "Server is ready to receive requests.")
				<-cancel
				return nil
			},
			func(err error) {
				close(cancel)
			},
		)
	}
	{
		cancel := make(chan struct{})
		g.Add(
			func() error {
				select {
				case <-reloadReady.C:
					break
				}

				// Any Stackdriver client initialization goes here.
				level.Info(logger).Log("msg", "Stackdriver client started")
				<-cancel
				return nil
			},
			func(err error) {
				if err := remoteStorage.Close(); err != nil {
					level.Error(logger).Log("msg", "Error stopping Stackdriver client", "err", err)
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

func reloadConfig(filename string, logger log.Logger, rls ...func(*config.Config) error) (err error) {
	level.Info(logger).Log("msg", "Loading configuration file", "filename", filename)

	defer func() {
		if err == nil {
			configSuccess.Set(1)
			configSuccessTime.Set(float64(time.Now().Unix()))
		} else {
			configSuccess.Set(0)
		}
	}()

	conf, err := config.LoadFile(filename)
	if err != nil {
		return fmt.Errorf("couldn't load configuration (--config.file=%s): %v", filename, err)
	}

	failed := false
	for _, rl := range rls {
		if err := rl(conf); err != nil {
			level.Error(logger).Log("msg", "Failed to apply configuration", "err", err)
			failed = true
		}
	}
	if failed {
		return fmt.Errorf("one or more errors occurred while applying the new configuration (--config.file=%s)", filename)
	}
	return nil
}

func startsOrEndsWithQuote(s string) bool {
	return strings.HasPrefix(s, "\"") || strings.HasPrefix(s, "'") ||
		strings.HasSuffix(s, "\"") || strings.HasSuffix(s, "'")
}

// computeExternalURL computes a sanitized external URL from a raw input. It infers unset
// URL parts from the OS and the given listen address.
func computeExternalURL(u, listenAddr string) (*url.URL, error) {
	if u == "" {
		hostname, err := os.Hostname()
		if err != nil {
			return nil, err
		}
		_, port, err := net.SplitHostPort(listenAddr)
		if err != nil {
			return nil, err
		}
		u = fmt.Sprintf("http://%s:%s/", hostname, port)
	}

	if startsOrEndsWithQuote(u) {
		return nil, fmt.Errorf("URL must not begin or end with quotes")
	}

	eu, err := url.Parse(u)
	if err != nil {
		return nil, err
	}

	ppref := strings.TrimRight(eu.Path, "/")
	if ppref != "" && !strings.HasPrefix(ppref, "/") {
		ppref = "/" + ppref
	}
	eu.Path = ppref

	return eu, nil
}
