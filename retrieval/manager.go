// Copyright 2013 The Prometheus Authors
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

package retrieval

import (
	"sync"

	"github.com/go-kit/kit/log"
	"github.com/go-kit/kit/log/level"

	"github.com/prometheus/prometheus/config"
)

// Appendable returns an Appender.
type Appendable interface {
	Appender() (Appender, error)
}

// NewPrometheusReader is the PrometheusReader constructor
func NewPrometheusReader(logger log.Logger, walDirectory string, app Appendable) *PrometheusReader {
	return &PrometheusReader{
		append:       app,
		logger:       logger,
		walDirectory: walDirectory,
		graceShut:    make(chan struct{}),
	}
}

type PrometheusReader struct {
	logger       log.Logger
	walDirectory string
	append       Appendable
	mtx          sync.RWMutex
	graceShut    chan struct{}
}

func (r *PrometheusReader) Run() error {
	level.Info(r.logger).Log("msg", "Starting Prometheus reader...")
	segmentsReader, err := wal.NewSegmentsReader(walDirectory)
	if err != nil {
		return err
	}
	reader := wal.NewReader(segmentsReader)
	for {
		select {
		case <-r.graceShut:
			return nil
		}
	}
}

// Stop cancels the reader and blocks until it has exited.
func (r *PrometheusReader) Stop() {
	close(r.graceShut)
}

// ApplyConfig resets the manager's target providers and job configurations as defined by the new cfg.
func (r *PrometheusReader) ApplyConfig(cfg *config.Config) error {
	r.mtx.Lock()
	defer r.mtx.Unlock()
	return nil
}
