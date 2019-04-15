// Copyright 2017 The Prometheus Authors
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

package main

import (
	"bytes"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-kit/kit/log"
)

var promPath string

func TestMain(m *testing.M) {
	flag.Parse()
	if testing.Short() {
		os.Exit(m.Run())
	}
	// On linux with a global proxy the tests will fail as the go client(http,grpc) tries to connect through the proxy.
	os.Setenv("no_proxy", "localhost,127.0.0.1,0.0.0.0,:")

	var err error
	promPath, err = os.Getwd()
	if err != nil {
		fmt.Printf("can't get current dir :%s \n", err)
		os.Exit(1)
	}
	promPath = filepath.Join(promPath, "prometheus")

	build := exec.Command("go", "build", "-o", promPath)
	output, err := build.CombinedOutput()
	if err != nil {
		fmt.Printf("compilation error :%s \n", output)
		os.Exit(1)
	}

	exitCode := m.Run()
	os.Remove(promPath)
	os.Exit(exitCode)
}

// As soon as prometheus starts responding to http request should be able to accept Interrupt signals for a gracefull shutdown.
func TestStartupInterrupt(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping test in short mode.")
	}

	prom := exec.Command(promPath, "--stackdriver.project-id=1234", "--prometheus.wal-directory=testdata/wal")
	var bout, berr bytes.Buffer
	prom.Stdout = &bout
	prom.Stderr = &berr
	err := prom.Start()
	if err != nil {
		t.Errorf("execution error: %v", err)
		return
	}

	done := make(chan error)
	go func() {
		done <- prom.Wait()
	}()

	var startedOk bool
	var stoppedErr error

Loop:
	for x := 0; x < 10; x++ {
		// error=nil means the sidecar has started so can send the interrupt signal and wait for the grace shutdown.
		if _, err := http.Get("http://localhost:9091/metrics"); err == nil {
			startedOk = true
			prom.Process.Signal(os.Interrupt)
			select {
			case stoppedErr = <-done:
				break Loop
			case <-time.After(10 * time.Second):
			}
			break Loop
		} else {
			select {
			case stoppedErr = <-done:
				break Loop
			default: // try again
			}
		}
		time.Sleep(500 * time.Millisecond)
	}

	t.Logf("stodut: %v\n", bout.String())
	t.Logf("stderr: %v\n", berr.String())
	if !startedOk {
		t.Errorf("prometheus didn't start in the specified timeout")
		return
	}
	if err := prom.Process.Kill(); err == nil {
		t.Errorf("prometheus didn't shutdown gracefully after sending the Interrupt signal")
	} else if stoppedErr != nil && stoppedErr.Error() != "signal: interrupt" { // TODO - find a better way to detect when the process didn't exit as expected!
		t.Errorf("prometheus exited with an unexpected error:%v", stoppedErr)
	}
}

func TestParseWhitelists(t *testing.T) {
	logger := log.NewLogfmtLogger(log.NewSyncWriter(os.Stderr))
	for _, tt := range []struct {
		name         string
		filtersets   []string
		filters      []string
		wantMatchers int
	}{
		{"both filters and filtersets defined",
			[]string{
				`metric_name`,
				`metric_name{label="value"}`,
			},
			[]string{
				`__name__="test1"`,
				`a1=~"test2.+"`,
				`a2!="test3"`,
				`a3!~"test4.*"`,
			}, 3},
		{"just filtersets", []string{"metric_name"}, []string{}, 1},
		{"just filters", []string{}, []string{`__name__="foo"`}, 1},
		{"neither filtersets nor filters", []string{}, []string{}, 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			// Test success cases.
			parsed, err := parseFiltersets(logger, tt.filtersets, tt.filters)
			if err != nil {
				t.Fatal(err)
			}
			if len(parsed) != tt.wantMatchers {
				t.Fatalf("expected %d matchers; got %d", tt.wantMatchers, len(parsed))
			}
		})
	}

	// Test failure cases.
	for _, tt := range []struct {
		name       string
		filtersets []string
		filters    []string
	}{
		{"Invalid character in key", []string{}, []string{`a-b="1"`}},
		{"Missing trailing quote", []string{}, []string{`a="1`}},
		{"Missing leading quote", []string{}, []string{`a=1"`}},
		{"Invalid operator", []string{}, []string{`a!=="1"`}},
		{"Invalid operator in filterset", []string{`{a!=="1"}`}, []string{}},
		{"Empty filterset", []string{""}, []string{}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := parseFiltersets(logger, tt.filtersets, tt.filters); err == nil {
				t.Fatalf("expected error, but got none")
			}
		})
	}
}
