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
		ConfigTable    map[string]string
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

	// We set these environment variables using the Kubernetes Downward API:
	// https://kubernetes.io/docs/tasks/inject-data-application/environment-variable-expose-pod-information/
	//
	// If they variables are not set, the template below will omit links
	// that depend on them.
	data.PodName = os.Getenv("POD_NAME")
	data.NodeName = os.Getenv("NODE_NAME")
	data.NamespaceName = os.Getenv("NAMESPACE_NAME")

	data.GKEInfo.ProjectId = h.projectId
	data.GKEInfo.ClusterLocation = h.cfg.kubernetesLabels.location
	data.GKEInfo.ClusterName = h.cfg.kubernetesLabels.clusterName

	data.ConfigTable = h.cfg.TableForStatusz()
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
    {{if (and .GKEInfo.ProjectId .GKEInfo.ClusterLocation .GKEInfo.ClusterName .PodName .NodeName .NamespaceName)}}
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
{{range $k, $v := .ConfigTable}}
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
