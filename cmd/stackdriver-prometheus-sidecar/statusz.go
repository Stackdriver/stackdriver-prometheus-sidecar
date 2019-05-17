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
	"html/template"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/go-kit/kit/log"
	"github.com/go-kit/kit/log/level"
	"github.com/prometheus/common/version"
	"github.com/prometheus/prometheus/scrape"
)

var (
	serverStart = time.Now()
)

type statuszHandler struct {
	logger    log.Logger
	projectId string
	cfg       *mainConfig
}

func (h *statuszHandler) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	var data struct {
		ServerName    string
		VersionInfo   string
		BuildContext  string
		Uname         string
		FdLimits      string
		StartTime     string
		Uptime        string
		PodName       string
		NodeName      string
		NamespaceName string
		GKEInfo       struct {
			ProjectId       string
			ClusterLocation string
			ClusterName     string
		}
		DisplayConfig  map[string]string
		MetricRenames  map[string]string
		StaticMetadata []scrape.MetricMetadata
	}

	data.ServerName = filepath.Base(os.Args[0])

	data.VersionInfo = version.Info()
	data.BuildContext = version.BuildContext()
	data.Uname = Uname()
	data.FdLimits = FdLimits()

	data.StartTime = serverStart.Format(time.RFC1123)
	uptime := int64(time.Since(serverStart).Seconds())
	data.Uptime = fmt.Sprintf("%d hr %02d min %02d sec",
		uptime/3600, (uptime/60)%60, uptime%60)

	data.PodName = os.Getenv("POD_NAME")
	data.NodeName = os.Getenv("NODE_NAME")
	data.NamespaceName = os.Getenv("NAMESPACE_NAME")

	data.GKEInfo.ProjectId = h.projectId
	data.GKEInfo.ClusterLocation = h.cfg.kubernetesLabels.location
	data.GKEInfo.ClusterName = h.cfg.kubernetesLabels.clusterName

	data.DisplayConfig = map[string]string{
		"Config filename":                 h.cfg.configFilename,
		"Filters":                         strings.Join(h.cfg.filters, ","),
		"Generic labels: location":        h.cfg.genericLabels.location,
		"Generic labels: namespace":       h.cfg.genericLabels.namespace,
		"Kubernetes labels: location":     h.cfg.kubernetesLabels.location,
		"Kubernetes labels: cluster name": h.cfg.kubernetesLabels.clusterName,
		"Listen address":                  h.cfg.listenAddress,
		"Log level":                       h.cfg.logLevel.String(),
		"Metrics prefix":                  h.cfg.metricsPrefix,
		"Project ID resource":             h.cfg.projectIdResource,
		"Prometheus URL":                  h.cfg.prometheusURL.String(),
		"Stackdriver address":             h.cfg.stackdriverAddress.String(),
		"Use GKE resource":                strconv.FormatBool(h.cfg.useGkeResource),
		"WAL directory":                   h.cfg.walDirectory,
	}
	data.MetricRenames = h.cfg.metricRenames
	data.StaticMetadata = h.cfg.staticMetadata

	if err := statuszTmpl.Execute(w, data); err != nil {
		level.Error(h.logger).Log("msg", "couldn't execute template", "err", err)
	}
}

var statuszTmpl = template.Must(template.New("").Parse(`
<!DOCTYPE html>
<html>
<head>
  <title>Status for {{.ServerName}}</title>
  <style>
    body {
      font-family: sans-serif;
    }
    h1 {
      clear: both;
      width: 100%;
      text-align: center;
      font-size: 120%;
      background: #eef;
    }
    h2 {
      font-size: 110%;
    }
    .lefthand {
      float: left;
      width: 80%;
    }
    .righthand {
      text-align: right;
    }
    td, th {
      background-color: rgba(0, 0, 0, 0.05);
    }
    th {
      text-align: left;
    }
  </style>
</head>

<body>
<h1>Status for {{.ServerName}}</h1>

<div>
  <div class=lefthand>
    Started: {{.StartTime}}<br>
    Up {{.Uptime}}<br>
    Version: {{.VersionInfo}}<br>
    Build context: {{.BuildContext}}<br>
    Host details: {{.Uname}}<br>
    FD limits: {{.FdLimits}}<br>
    {{if (and .GKEInfo.ProjectId .GKEInfo.ClusterLocation .GKEInfo.ClusterName)}}
    <p>
    Pod <a href="https://console.cloud.google.com/kubernetes/pod/{{.GKEInfo.ClusterLocation}}/{{.GKEInfo.ClusterName}}/{{if .NamespaceName}}{{.NamespaceName}}{{else}}default{{end}}/{{.PodName}}?project={{.GKEInfo.ProjectId}}">{{.PodName}}</a><br>
    Node <a href="https://console.cloud.google.com/kubernetes/node/{{.GKEInfo.ClusterLocation}}/{{.GKEInfo.ClusterName}}/{{.NodeName}}?project={{.GKEInfo.ProjectId}}">{{.NodeName}}</a><br>
    Cluster <a href="https://console.cloud.google.com/kubernetes/clusters/details/{{.GKEInfo.ClusterLocation}}/{{.GKEInfo.ClusterName}}?project={{.GKEInfo.ProjectId}}">{{.GKEInfo.ClusterName}}</a>
    </p>
    {{end}}
  </div>
  <div class=righthand>
    View <a href=/metrics>metrics</a><br>
  </div>
</div>

<h1>Parsed configuration</h1>

<table>
{{range $k, $v := .DisplayConfig}}
<tr><th>{{$k}}</th><td>{{$v}}</td></tr>
{{end}}
</table>

{{if .MetricRenames}}
<h2>Metric renames</h2>
<table>
<tr><th>from</th><th>to</th></tr>
{{range $from, $to := .MetricRenames}}
<tr><td>{{$from}}</td><td>{{$to}}</td></tr>
{{end}}
</table>
{{end}}

{{if .StaticMetadata}}
<h2>Static metadata</h2>
<table>
<tr><th>metric</th><th>type</th><th>help</th></tr>
{{range .StaticMetadata}}
<tr><td>{{.Metric}}</td><td>{{.Type}}</td><td>{{.Help}}</td></tr>
{{end}}
</table>
{{end}}

</body>
</html>
`))
