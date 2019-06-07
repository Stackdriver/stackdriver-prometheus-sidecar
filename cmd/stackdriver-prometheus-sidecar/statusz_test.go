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

	"github.com/go-kit/kit/log"
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

	handler := &statuszHandler{
		logger:    log.NewLogfmtLogger(os.Stdout),
		projectId: "my-project",
		cfg: &mainConfig{
			kubernetesLabels: kubernetesConfig{
				location:    "us-central1-a",
				clusterName: "my-cluster",
			},
			prometheusURL:      mustParseURL(t, "http://127.0.0.1:9090/"),
			stackdriverAddress: mustParseURL(t, "https://monitoring.googleapis.com:443/"),
			walDirectory:       "/data/wal",
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
		regexp.MustCompile(`<tr><th>WAL directory</th><td>/data/wal</td></tr>`),
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
