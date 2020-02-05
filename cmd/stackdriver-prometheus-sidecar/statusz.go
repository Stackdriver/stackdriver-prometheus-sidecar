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

//go:generate statik -f -src=./  -include=*.html
package main

import (
	"html/template"
	"io/ioutil"
	"net/http"
	"os"
	"path/filepath"
	"time"

	_ "github.com/Stackdriver/stackdriver-prometheus-sidecar/cmd/stackdriver-prometheus-sidecar/statik"
	"github.com/go-kit/kit/log"
	"github.com/go-kit/kit/log/level"
	"github.com/prometheus/common/version"
	"github.com/rakyll/statik/fs"
)

var (
	serverStart = time.Now()
	statuszTmpl *template.Template
)

type statuszHandler struct {
	logger    log.Logger
	projectID string
	cfg       *mainConfig
}

func init() {
	statikFS, err := fs.New()
	if err != nil {
		panic(err)
	}
	statuszFile, err := statikFS.Open("/statusz-tmpl.html")
	if err != nil {
		panic(err)
	}
	contents, err := ioutil.ReadAll(statuszFile)
	if err != nil {
		panic(err)
	}
	statuszTmpl, err = template.New("statusz-tmpl.html").Parse(string(contents))
	if err != nil {
		panic(err)
	}
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
			ProjectID       string
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

	data.GKEInfo.ProjectID = h.projectID
	data.GKEInfo.ClusterLocation = h.cfg.KubernetesLabels.Location
	data.GKEInfo.ClusterName = h.cfg.KubernetesLabels.ClusterName

	data.Config = h.cfg

	if err := statuszTmpl.Execute(w, data); err != nil {
		level.Error(h.logger).Log("msg", "couldn't execute template", "err", err)
	}
}
