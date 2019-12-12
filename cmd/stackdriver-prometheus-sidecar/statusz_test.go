// Copyright 2019 Google Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     https://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
package main

import (
	"fmt"
	"io/ioutil"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"testing"

	"github.com/Stackdriver/stackdriver-prometheus-sidecar/metadata"
	"github.com/Stackdriver/stackdriver-prometheus-sidecar/retrieval"
	"github.com/go-kit/kit/log"
	"github.com/prometheus/common/promlog"
	"github.com/prometheus/prometheus/pkg/labels"
	"github.com/prometheus/prometheus/pkg/textparse"
	metric_pb "google.golang.org/genproto/googleapis/api/metric"
)

func scrapeStatusz(handler *statuszHandler) ([]byte, error) {
	req := httptest.NewRequest("GET", "http://localhost/statusz", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	resp := w.Result()
	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading body failed: %v", err)
	}
	return body, nil
}

func mustParseURL(t *testing.T, rawurl string) *url.URL {
	u, err := url.Parse(rawurl)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func TestStatuszHandler(t *testing.T) {
	os.Setenv("POD_NAME", "my-pod")
	os.Setenv("NODE_NAME", "my-node")
	os.Setenv("NAMESPACE_NAME", "my-namespace")

	matcher, _ := labels.NewMatcher(labels.MatchEqual, "k", "v")

	logConfig := promlog.Config{Level: &promlog.AllowedLevel{}}
	logConfig.Level.Set("debug")

	handler := &statuszHandler{
		logger:    log.NewLogfmtLogger(os.Stdout),
		projectID: "my-project",
		cfg: &mainConfig{
			Aggregations: retrieval.CounterAggregatorConfig{
				"aggmetric1": {Matchers: [][]*labels.Matcher{{matcher}}},
				"aggmetric2": {Matchers: [][]*labels.Matcher{{matcher}}},
			},
			ConfigFilename: "/my/config",
			Filters:        []string{"filter1", "filter2"},
			Filtersets:     []string{"filterset1", "filterset2"},
			GenericLabels: genericConfig{
				Location:  "asia-east2",
				Namespace: "my-namespace",
			},
			KubernetesLabels: kubernetesConfig{
				Location:    "us-central1-a",
				ClusterName: "my-cluster",
			},
			ListenAddress:      "0.0.0.0:9091",
			PromlogConfig:      logConfig,
			MetricRenames:      map[string]string{"from1": "to1", "from2": "to2"},
			MetricsPrefix:      "external.googleapis.com/prometheus",
			MonitoringBackends: []string{"prometheus", "stackdriver"},
			ProjectIDResource:  "my-project",
			PrometheusURL:      mustParseURL(t, "http://127.0.0.1:9090/"),
			StackdriverAddress: mustParseURL(t, "https://monitoring.googleapis.com:443/"),
			StaticMetadata: []*metadata.Entry{
				&metadata.Entry{Metric: "metric1", MetricType: textparse.MetricType("type1"), ValueType: metric_pb.MetricDescriptor_INT64},
				&metadata.Entry{Metric: "metric2", MetricType: textparse.MetricType("type2"), ValueType: metric_pb.MetricDescriptor_DOUBLE},
			},
			StoreInFilesDirectory: "/my/files/directory",
			UseGKEResource:        true,
			UseRestrictedIPs:      true,
			WALDirectory:          "/data/wal",
		},
	}

	body, err := scrapeStatusz(handler)
	if err != nil {
		t.Fatalf("Failed scraping the statusz page: %v", err)
	}

	mustMatch := []*regexp.Regexp{
		regexp.MustCompile(`h1.*Status for stackdriver-prometheus-sidecar`),
		regexp.MustCompile(`https://console.cloud.google.com/kubernetes/pod/us-central1-a/my-cluster/my-namespace/my-pod\?project=my-project`),
		regexp.MustCompile(`https://console.cloud.google.com/kubernetes/node/us-central1-a/my-cluster/my-node\?project=my-project`),
		regexp.MustCompile(`https://console.cloud.google.com/kubernetes/clusters/details/us-central1-a/my-cluster\?project=my-project`),

		// Parsed configuration
		regexp.MustCompile(`<tr><th>Config filename</th><td>/my/config</td></tr>`),
		regexp.MustCompile(`<tr><th>Filters</th><td>\[filter1 filter2\]</td></tr>`),
		regexp.MustCompile(`<tr><th>Filter sets</th><td>\[filterset1 filterset2\]</td></tr>`),
		regexp.MustCompile(`<tr><th>Generic labels: location</th><td>asia-east2</td></tr>`),
		regexp.MustCompile(`<tr><th>Generic labels: namespace</th><td>my-namespace</td></tr>`),
		regexp.MustCompile(`<tr><th>Kubernetes labels: cluster name</th><td>my-cluster</td></tr>`),
		regexp.MustCompile(`<tr><th>Kubernetes labels: location</th><td>us-central1-a</td></tr>`),
		regexp.MustCompile(`<tr><th>Listen address</th><td>0.0.0.0:9091</td></tr>`),
		regexp.MustCompile(`<tr><th>Log level</th><td>debug</td></tr>`),
		regexp.MustCompile(`<tr><th>Log format</th><td>&lt;nil&gt;</td></tr>`),
		regexp.MustCompile(`<tr><th>Metrics prefix</th><td>external.googleapis.com/prometheus</td></tr>`),
		regexp.MustCompile(`<tr><th>Monitoring backends</th><td>\[prometheus stackdriver\]</td></tr>`),
		regexp.MustCompile(`<tr><th>Project ID resource</th><td>my-project</td></tr>`),
		regexp.MustCompile(`<tr><th>Prometheus URL</th><td>http://127.0.0.1:9090/</td></tr>`),
		regexp.MustCompile(`<tr><th>Stackdriver address</th><td>https://monitoring.googleapis.com:443/</td></tr>`),
		regexp.MustCompile(`<tr><th>Store in files directory</th><td>/my/files/directory</td></tr>`),
		regexp.MustCompile(`<tr><th>Use GKE resource</th><td>true</td></tr>`),
		regexp.MustCompile(`<tr><th>Use restricted IPs</th><td>true</td></tr>`),
		regexp.MustCompile(`<tr><th>WAL directory</th><td>/data/wal</td></tr>`),
		// for metric renames
		regexp.MustCompile(`<h2>Metric renames</h2>`),
		regexp.MustCompile(`<tr><td>from1</td><td>to1</td></tr>`),
		regexp.MustCompile(`<tr><td>from2</td><td>to2</td></tr>`),
		// for static metadata
		regexp.MustCompile(`<h2>Static metadata</h2>`),
		regexp.MustCompile(`<tr><td>metric1</td><td>type1</td><td>INT64</td></tr>`),
		regexp.MustCompile(`<tr><td>metric2</td><td>type2</td><td>DOUBLE</td></tr>`),
		// for aggregations
		regexp.MustCompile(`<h2>Aggregations</h2>`),
		regexp.MustCompile(`<tr><td>aggmetric1</td><td>\[\[k=.*v.*\]\]</td></tr>`),
		regexp.MustCompile(`<tr><td>aggmetric2</td><td>\[\[k=.*v.*\]\]</td></tr>`),

		// closing </html> ensures the template completed execution
		regexp.MustCompile(`(?m)^</html>$`),
	}
	logged := false
	for _, re := range mustMatch {
		if !re.Match(body) {
			if !logged {
				t.Logf("Body:\n%s", body)
				logged = true
			}
			t.Errorf("Failed matching %v", re)
		}
	}
}
