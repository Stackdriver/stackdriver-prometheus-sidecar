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

package metadata

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/pkg/errors"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/prometheus/pkg/textparse"
	"github.com/prometheus/prometheus/scrape"
)

// Cache populates and maintains a cache of metric metadata it retrieves
// from a given Prometheus server.
// Its methods are not safe for concurrent use.
type Cache struct {
	promURL *url.URL
	client  *http.Client

	requests *prometheus.CounterVec
	errors   *prometheus.CounterVec
	latency  *prometheus.HistogramVec

	metadata map[string]*metadataEntry
	seenJobs map[string]struct{}
}

// DefaultEndpointPath is the default HTTP path on which Prometheus serves
// the target metadata endpoint.
const DefaultEndpointPath = "/api/v1/targets/metadata"

// NewCache returns a new cache that gets populated by the metadata endpoint
// at the given URL.
// It uses the default endpoint path if no specific path is provided.
func NewCache(reg prometheus.Registerer, client *http.Client, promURL *url.URL) *Cache {
	if client == nil {
		client = http.DefaultClient
	}
	c := &Cache{
		promURL:  promURL,
		client:   client,
		metadata: map[string]*metadataEntry{},
		seenJobs: map[string]struct{}{},
	}

	c.requests = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "sidecar_metadata_requests_total",
		Help: "Total requests made to the target metadata API",
	}, []string{"type", "status"})

	c.errors = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "sidecar_metadata_request_errors_total",
		Help: "Total failing requests to the target metadata API",
	}, []string{"type", "status"})

	c.latency = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name: "sidecar_metadata_reuqest_latency",
		Help: "Reuqest latency to the target metadata API",
	}, []string{"type", "status"})

	if reg != nil {
		reg.MustRegister(c.requests, c.errors, c.latency)
	}
	return c
}

const retryInterval = 30 * time.Second

type metadataEntry struct {
	scrape.MetricMetadata
	found     bool
	lastFetch time.Time
}

func (e *metadataEntry) shouldRefetch() bool {
	// TODO(fabxc): how often does this happen? Do we need an exponential backoff?
	return !e.found && time.Since(e.lastFetch) > retryInterval
}

// Get returns metadata for the given metric and job. If the metadata
// is not in the cache, it blocks until we have retrieved it from the Prometheus server.
// If the metadata cannot be found, nil is returned.
func (c *Cache) Get(ctx context.Context, job, instance, metric string) (*scrape.MetricMetadata, error) {
	md, ok := c.metadata[metric]
	if !ok || md.shouldRefetch() {
		// If we are seeing the job for the first time, preemptively get a full
		// list of all metadata for the instance.
		if _, ok := c.seenJobs[job]; !ok {
			mds, err := c.fetchBatch(ctx, job, instance)
			if err != nil {
				return nil, errors.Wrapf(err, "fetch metadata for job %q", job)
			}
			for _, md := range mds {
				// Only set if we haven't seen the metric before. Changes to metadata
				// may need special handling in Stackdriver, which we do not provide
				// yet anyway.
				if _, ok := c.metadata[md.Metric]; !ok {
					c.metadata[md.Metric] = md
				}
			}
			c.seenJobs[job] = struct{}{}
		} else {
			md, err := c.fetchMetric(ctx, job, instance, metric)
			if err != nil {
				return nil, errors.Wrapf(err, "fetch metric metadata \"%s/%s/%s\"", job, instance, metric)
			}
			c.metadata[metric] = md
		}
		md = c.metadata[metric]
	}
	if md == nil || !md.found {
		return nil, nil
	}
	return &md.MetricMetadata, nil
}

func (c *Cache) fetch(ctx context.Context, typ string, q url.Values) (*apiResponse, error) {
	u := *c.promURL
	u.RawQuery = q.Encode()

	req, err := http.NewRequest("GET", u.String(), nil)
	if err != nil {
		return nil, errors.Wrap(err, "build request")
	}
	req = req.WithContext(ctx)
	start := time.Now()

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, errors.Wrap(err, "query Prometheus")
	}
	defer resp.Body.Close()

	code := strconv.Itoa(resp.StatusCode)
	if err != nil {
		c.errors.WithLabelValues(typ, code).Inc()
	}
	c.requests.WithLabelValues(typ, code).Inc()
	c.latency.WithLabelValues(typ, code).Observe(time.Since(start).Seconds())

	var apiResp apiResponse
	if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
		return nil, errors.Wrap(err, "decode response")
	}
	return &apiResp, nil
}

const apiErrorNotFound = "not_found"

// fetchMetric fetches metadata for the given job, instance, and metric combination.
// It returns a not-found entry if the fetch is successful but returns no data.
func (c *Cache) fetchMetric(ctx context.Context, job, instance, metric string) (*metadataEntry, error) {
	job, instance = escapeLval(job), escapeLval(instance)

	apiResp, err := c.fetch(ctx, "metric", url.Values{
		"match_target": []string{fmt.Sprintf("{job=\"%s\",instance=\"%s\"}", job, instance)},
		"metric":       []string{metric},
	})
	if err != nil {
		return nil, err
	}
	now := time.Now()

	if apiResp.ErrorType != "" && apiResp.ErrorType != apiErrorNotFound {
		return nil, errors.Wrap(errors.New(apiResp.Error), "lookup failed")
	}
	if len(apiResp.Data) == 0 {
		return &metadataEntry{lastFetch: now}, nil
	}
	d := apiResp.Data[0]

	return &metadataEntry{
		MetricMetadata: scrape.MetricMetadata{
			Metric: metric,
			Type:   d.Type,
			Help:   d.Help,
		},
		lastFetch: now,
		found:     true,
	}, nil
}

// fetchBatch fetches all metric metadata for the given job and instance combination.
// We constrain it by instance to reduce the total payload size.
// In a well-configured setup it is unlikely that instances for the same job have any notable
// difference in their exposed metrics.
func (c *Cache) fetchBatch(ctx context.Context, job, instance string) (map[string]*metadataEntry, error) {
	job, instance = escapeLval(job), escapeLval(instance)

	apiResp, err := c.fetch(ctx, "batch", url.Values{
		"match_target": []string{fmt.Sprintf("{job=\"%s\",instance=\"%s\"}", job, instance)},
	})
	if err != nil {
		return nil, err
	}
	now := time.Now()

	if apiResp.ErrorType == apiErrorNotFound {
		return nil, nil
	}
	if apiResp.ErrorType != "" {
		return nil, errors.Wrap(errors.New(apiResp.Error), "lookup failed")
	}
	// Pre-allocate for all received data plus internal metrics.
	result := make(map[string]*metadataEntry, len(apiResp.Data)+len(internalMetrics))

	for _, md := range apiResp.Data {
		result[md.Metric] = &metadataEntry{
			MetricMetadata: scrape.MetricMetadata{
				Metric: md.Metric,
				Type:   md.Type,
				Help:   md.Help,
			},
			lastFetch: now,
			found:     true,
		}
	}
	// Prometheus's scraping layer writes a few internal metrics, which we won't get
	// metadata for via the API. We populate hardcoded metadata for them.
	for _, md := range internalMetrics {
		result[md.Metric] = &metadataEntry{MetricMetadata: md, lastFetch: now, found: true}
	}
	return result, nil
}

var internalMetrics = map[string]scrape.MetricMetadata{
	"up": {
		Metric: "up",
		Type:   textparse.MetricTypeGauge,
		Help:   "Up indicates whether the last target scrape was successful",
	},
	"scrape_samples_scraped": {
		Metric: "scrape_samples_scraped",
		Type:   textparse.MetricTypeGauge,
		Help:   "How many samples were scraped during the last successful scrape",
	},
	"scrape_duration_seconds": {
		Metric: "scrape_duration_seconds",
		Type:   textparse.MetricTypeGauge,
		Help:   "Duration of the last scrape",
	},
	"scrape_samples_post_metric_relabeling": {
		Metric: "scrape_samples_post_metric_relabeling",
		Type:   textparse.MetricTypeGauge,
		Help:   "How many samples were ingested after relabeling",
	},
}

type apiResponse struct {
	Status    string        `json:"status"`
	Data      []apiMetadata `json:"data"`
	Error     string        `json:"error"`
	ErrorType string        `json:"errorType"`
}

type apiMetadata struct {
	// We do not decode the target information.
	Metric string               `json:"metric"`
	Help   string               `json:"help"`
	Type   textparse.MetricType `json:"type"`
}

var lvalReplacer = strings.NewReplacer(
	`\"`, "\"",
	`\\`, "\\",
	`\n`, "\n",
)

// escapeLval escapes a label value.
func escapeLval(s string) string {
	if strings.IndexByte(s, byte('\\')) >= 0 {
		return lvalReplacer.Replace(s)
	}
	return s
}
