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

package stackdriver

import (
	"fmt"
	"net"
	"net/url"
	"strings"
	"testing"
	"time"

	config_util "github.com/prometheus/common/config"
	"github.com/prometheus/common/model"
	monitoring "google.golang.org/genproto/googleapis/monitoring/v3"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

var longErrMessage = strings.Repeat("[error message]", 10)

func newLocalListener() net.Listener {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		if l, err = net.Listen("tcp6", "[::1]:0"); err != nil {
			panic(fmt.Sprintf("httptest: failed to listen on a port: %v", err))
		}
	}
	return l
}

func TestStoreErrorHandling(t *testing.T) {
	tests := []struct {
		code        int
		status      *status.Status
		recoverable bool
	}{
		{
			status: nil,
		},
		{
			status:      status.New(codes.NotFound, longErrMessage),
			recoverable: false,
		},
		{
			status:      status.New(codes.Unavailable, longErrMessage),
			recoverable: true,
		},
	}

	for i, test := range tests {
		t.Run(fmt.Sprintf("%d", i), func(t *testing.T) {
			listener := newLocalListener()
			grpcServer := grpc.NewServer()
			monitoring.RegisterMetricServiceServer(grpcServer, &metricServiceServer{test.status})
			go grpcServer.Serve(listener)
			defer grpcServer.Stop()

			serverURL, err := url.Parse("https://" + listener.Addr().String() + "?auth=false")
			if err != nil {
				t.Fatal(err)
			}

			c := NewClient(0, &ClientConfig{
				URL:     &config_util.URL{URL: serverURL},
				Timeout: model.Duration(time.Second),
			})

			err = c.Store(&monitoring.CreateTimeSeriesRequest{
				TimeSeries: []*monitoring.TimeSeries{
					&monitoring.TimeSeries{},
				},
			})
			if test.status != nil {
				rerr, recoverable := err.(recoverableError)
				if recoverable != test.recoverable {
					if test.recoverable {
						t.Errorf("expected recoverableError in error %v", err)
					} else {
						t.Errorf("unexpected recoverableError in error %v", err)
					}
				}
				if recoverable {
					err = rerr.error
				}
				status := status.Convert(err)
				if status.Code() != test.status.Code() || status.Message() != test.status.Message() {
					t.Errorf("expected status '%v', got '%v'", test.status.Err(), status.Err())
				}
			}
		})
	}
}

func TestEmptyRequest(t *testing.T) {
	serverURL, err := url.Parse("http://localhost:12345")
	if err != nil {
		t.Fatal(err)
	}
	c := NewClient(0, &ClientConfig{
		URL:     &config_util.URL{URL: serverURL},
		Timeout: model.Duration(time.Second),
	})
	if err := c.Store(&monitoring.CreateTimeSeriesRequest{}); err != nil {
		t.Fatal(err)
	}
}
