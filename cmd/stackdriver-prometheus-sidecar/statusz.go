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
	"html/template"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/go-kit/kit/log"
	"github.com/go-kit/kit/log/level"
	"github.com/prometheus/common/version"
)

const(
     statuszHtml = `	
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
      <div class="lefthand">
        Started: {{.StartTime}}<br>
        Up {{.Uptime}}<br>
        Version: {{.VersionInfo}}<br>
        Build context: {{.BuildContext}}<br>
        Host details: {{.Uname}}<br>
        FD limits: {{.FdLimits}}<br>
        {{if (and .GKEInfo.ProjectId .GKEInfo.ClusterLocation .GKEInfo.ClusterName .PodName .NodeName .NamespaceName)}}
        <p>
          Pod <a href="https://console.cloud.google.com/kubernetes/pod/{{.GKEInfo.ClusterLocation}}/{{.GKEInfo.ClusterName}}/{{if .NamespaceName}}{{.NamespaceName}}{{else}}default{{end}}/{{.PodName}}?project={{.GKEInfo.ProjectId}}">{{.PodName}}</a><br>
          Node <a href="https://console.cloud.google.com/kubernetes/node/{{.GKEInfo.ClusterLocation}}/{{.GKEInfo.ClusterName}}/{{.NodeName}}?project={{.GKEInfo.ProjectId}}">{{.NodeName}}</a><br>
          Cluster <a href="https://console.cloud.google.com/kubernetes/clusters/details/{{.GKEInfo.ClusterLocation}}/{{.GKEInfo.ClusterName}}?project={{.GKEInfo.ProjectId}}">{{.GKEInfo.ClusterName}}</a>
        </p>
        {{end}}
      </div>
      <div class="righthand">
        View <a href="/metrics">metrics</a><br>
      </div>
    </div>

    <h1>Parsed configuration</h1>

    {{with .Config}}

    <table>
      <tr><th>Config filename</th><td>{{.ConfigFilename}}</td></tr>
      <tr><th>Filters</th><td>{{.Filters}}</td></tr>
      <tr><th>Filter sets</th><td>{{.Filtersets}}</td></tr>
      <tr><th>Generic labels: location</th><td>{{.GenericLabels.Location}}</td></tr>
      <tr><th>Generic labels: namespace</th><td>{{.GenericLabels.Namespace}}</td></tr>
      <tr><th>Kubernetes labels: cluster name</th><td>{{.KubernetesLabels.ClusterName}}</td></tr>
      <tr><th>Kubernetes labels: location</th><td>{{.KubernetesLabels.Location}}</td></tr>
      <tr><th>Listen address</th><td>{{.ListenAddress}}</td></tr>
      <tr><th>Log level</th><td>{{.PromlogConfig.Level}}</td></tr>
      <tr><th>Log format</th><td>{{.PromlogConfig.Format}}</td></tr>
      <tr><th>Metrics prefix</th><td>{{.MetricsPrefix}}</td></tr>
      <tr><th>Monitoring backends</th><td>{{.MonitoringBackends}}</td></tr>
      <tr><th>Project ID resource</th><td>{{.ProjectIDResource}}</td></tr>
      <tr><th>Prometheus URL</th><td>{{.PrometheusURL}}</td></tr>
      <tr><th>Stackdriver address</th><td>{{.StackdriverAddress}}</td></tr>
      <tr><th>Store in files directory</th><td>{{.StoreInFilesDirectory}}</td></tr>
      <tr><th>Use GKE resource</th><td>{{.UseGKEResource}}</td></tr>
      <tr><th>Use restricted IPs</th><td>{{.UseRestrictedIPs}}</td></tr>
      <tr><th>WAL directory</th><td>{{.WALDirectory}}</td></tr>
    </table>

    <h2>Aggregations</h2>
    {{if .Aggregations}}
    <table>
      <tr><th>metric</th><th>matchers</th></tr>
      {{range $metric, $metricConfig := .Aggregations}}
      <tr><td>{{$metric}}</td><td>{{$metricConfig.Matchers}}</td></tr>
      {{end}}
    </table>
    {{else}}
    none
    {{end}}

    <h2>Metric renames</h2>
    {{if .MetricRenames}}
    <table>
      <tr><th>from</th><th>to</th></tr>
      {{range $from, $to := .MetricRenames}}
      <tr><td>{{$from}}</td><td>{{$to}}</td></tr>
      {{end}}
    </table>
    {{else}}
    none
    {{end}}

    <h2>Static metadata</h2>
    {{if .StaticMetadata}}
    <table>
      <tr><th>metric</th><th>type</th><th>unit</th></tr>
      {{range .StaticMetadata}}
      <tr><td>{{.Metric}}</td><td>{{.MetricType}}</td><td>{{.ValueType}}</td></tr>
      {{end}}
    </table>
    {{else}}
    none
    {{end}}

    {{end}}

  </body>
</html>
`
)
var (
	serverStart = time.Now()
	statuszTmpl = template.Must(template.New("statusz-tmpl.html").Parse(statuszHtml))
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
		StartTime     time.Time
		Uptime        time.Duration
		PodName       string
		NodeName      string
		NamespaceName string
		GKEInfo       struct {
			ProjectId       string
			ClusterLocation string
			ClusterName     string
		}
		Config *mainConfig
	}
	data.ServerName = filepath.Base(os.Args[0])

	data.VersionInfo = version.Info()
	data.BuildContext = version.BuildContext()
	data.Uname = Uname()
	data.FdLimits = FdLimits()

	data.StartTime = serverStart
	data.Uptime = time.Since(serverStart)

	// We set these environment variables using the Kubernetes Downward API:
	// https://kubernetes.io/docs/tasks/inject-data-application/environment-variable-expose-pod-information/
	//
	// If they variables are not set, the template below will omit links
	// that depend on them.
	data.PodName = os.Getenv("POD_NAME")
	data.NodeName = os.Getenv("NODE_NAME")
	data.NamespaceName = os.Getenv("NAMESPACE_NAME")

	data.GKEInfo.ProjectId = h.projectId
	data.GKEInfo.ClusterLocation = h.cfg.KubernetesLabels.Location
	data.GKEInfo.ClusterName = h.cfg.KubernetesLabels.ClusterName

	data.Config = h.cfg

	if err := statuszTmpl.Execute(w, data); err != nil {
		level.Error(h.logger).Log("msg", "couldn't execute template", "err", err)
	}
}
