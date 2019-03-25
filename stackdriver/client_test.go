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
	"context"
	"fmt"
	"net"
	"net/url"
	"strings"
	"testing"
	"time"

	monitoring "google.golang.org/genproto/googleapis/monitoring/v3"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/resolver"
	"google.golang.org/grpc/resolver/manual"
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

			c := NewClient(&ClientConfig{
				URL:     serverURL,
				Timeout: time.Second,
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
	c := NewClient(&ClientConfig{
		URL:     serverURL,
		Timeout: time.Second,
	})
	if err := c.Store(&monitoring.CreateTimeSeriesRequest{}); err != nil {
		t.Fatal(err)
	}
}

func TestResolver(t *testing.T) {
  grpcServer := grpc.NewServer()
	listener := newLocalListener()
	go grpcServer.Serve(listener)
	defer grpcServer.Stop()

	serverURL, err := url.Parse("http://stackdriver.invalid?auth=false")
	if err != nil {
		t.Fatal(err)
	}

	res, _ := manual.GenerateAndRegisterManualResolver()
	res.InitialAddrs([]resolver.Address{
		{Addr: "localhost"},
	})
	c := NewClient(&ClientConfig{
		URL:      serverURL,
		Timeout:  time.Second,
		Resolver: res,
	})

  address := c.url.Hostname()
	ctx, cancel := context.WithTimeout(context.Background(), c.timeout)
	defer cancel()

	c.resolver.Scheme()

	conn, connerr := grpc.DialContext(ctx, address, grpc.WithInsecure())
	c.conn = conn
	defer c.conn.Close()
	if connerr != nil {
		t.Fatal(connerr)
	}

	requestedTarget := c.conn.Target()
	if requestedTarget != "stackdriver.invalid" {
		t.Errorf("ERROR: Remote address is %s, want stackdriver.invalid.",
			requestedTarget)
	}
}
