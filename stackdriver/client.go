// Copyright 2016 The Prometheus Authors
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
	"crypto/tls"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"sync"
	"time"

	"go.opencensus.io/plugin/ocgrpc"
	monitoring "google.golang.org/genproto/googleapis/monitoring/v3"
	"google.golang.org/grpc"
	"google.golang.org/grpc/balancer/roundrobin"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/oauth"
	"google.golang.org/grpc/resolver/manual"
	"google.golang.org/grpc/status"

	"github.com/go-kit/kit/log"
	"github.com/go-kit/kit/log/level"
	"github.com/prometheus/common/version"
)

const (
	MaxTimeseriesesPerRequest = 200
	MonitoringWriteScope      = "https://www.googleapis.com/auth/monitoring.write"
)

// Client allows reading and writing from/to a remote gRPC endpoint. The
// implementation may hit a single backend, so the application should create a
// number of these clients.
type Client struct {
	logger    log.Logger
	projectId string
	url       *url.URL
	timeout   time.Duration
	resolver  *manual.Resolver

	conn *grpc.ClientConn
}

// ClientConfig configures a Client.
type ClientConfig struct {
	Logger    log.Logger
	ProjectId string // The Stackdriver project id in "projects/name-or-number" format.
	URL       *url.URL
	Timeout   time.Duration
	Resolver  *manual.Resolver
}

// NewClient creates a new Client.
func NewClient(conf *ClientConfig) *Client {
	logger := conf.Logger
	if logger == nil {
		logger = log.NewNopLogger()
	}
	return &Client{
		logger:    logger,
		projectId: conf.ProjectId,
		url:       conf.URL,
		timeout:   conf.Timeout,
		resolver:  conf.Resolver,
	}
}

type recoverableError struct {
	error
}

// version.* is populated for 'promu' builds, so this will look broken in unit tests.
var userAgent = fmt.Sprintf("StackdriverPrometheus/%s", version.Version)

func (c *Client) getConnection(ctx context.Context) (*grpc.ClientConn, error) {
	if c.conn != nil {
		return c.conn, nil
	}

	useAuth, err := strconv.ParseBool(c.url.Query().Get("auth"))
	if err != nil {
		useAuth = true // Default to auth enabled.
	}
	level.Debug(c.logger).Log(
		"msg", "is auth enabled",
		"auth", useAuth,
		"url", c.url.String())
	// Google APIs currently return a single IP for the whole service.  gRPC
	// client-side load-balancing won't spread the load across backends
	// while that's true, but it also doesn't hurt.
	dopts := []grpc.DialOption{
		grpc.WithBalancerName(roundrobin.Name),
		grpc.WithBlock(), // Wait for the connection to be established before using it.
		grpc.WithUserAgent(userAgent),
		grpc.WithStatsHandler(&ocgrpc.ClientHandler{}),
	}
	if useAuth {
		rpcCreds, err := oauth.NewApplicationDefault(context.Background(), MonitoringWriteScope)
		if err != nil {
			return nil, err
		}
		tlsCreds := credentials.NewTLS(&tls.Config{})
		dopts = append(dopts,
			grpc.WithTransportCredentials(tlsCreds),
			grpc.WithPerRPCCredentials(rpcCreds))
	} else {
		dopts = append(dopts, grpc.WithInsecure())
	}
	address := c.url.Hostname()
	if len(c.url.Port()) > 0 {
		address = net.JoinHostPort(address, c.url.Port())
	}
	if c.resolver != nil {
		address = c.resolver.Scheme() + ":///" + address
	}
	conn, err := grpc.DialContext(ctx, address, dopts...)
	c.conn = conn
	if err == context.DeadlineExceeded {
		return conn, recoverableError{err}
	}
	return conn, err
}

// Store sends a batch of samples to the HTTP endpoint.
func (c *Client) Store(req *monitoring.CreateTimeSeriesRequest) error {
	tss := req.TimeSeries
	if len(tss) == 0 {
		// Nothing to do, return silently.
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), c.timeout)
	defer cancel()

	conn, err := c.getConnection(ctx)
	if err != nil {
		return err
	}

	service := monitoring.NewMetricServiceClient(conn)

	errors := make(chan error, len(tss)/MaxTimeseriesesPerRequest+1)
	var wg sync.WaitGroup
	for i := 0; i < len(tss); i += MaxTimeseriesesPerRequest {
		end := i + MaxTimeseriesesPerRequest
		if end > len(tss) {
			end = len(tss)
		}
		wg.Add(1)
		go func(begin int, end int) {
			defer wg.Done()
			req_copy := &monitoring.CreateTimeSeriesRequest{
				Name:       c.projectId,
				TimeSeries: req.TimeSeries[begin:end],
			}
			_, err := service.CreateTimeSeries(ctx, req_copy)
			if err != nil {
				level.Debug(c.logger).Log(
					"msg", "Partial failure calling CreateTimeSeries",
					"err", err)
				status, ok := status.FromError(err)
				if !ok {
					level.Warn(c.logger).Log("msg", "Unexpected error message type from Monitoring API", "err", err)
					errors <- err
					return
				}
				switch status.Code() {
				case codes.DeadlineExceeded, codes.Unavailable:
					errors <- recoverableError{err}
				default:
					errors <- err
				}
			}
		}(i, end)
	}
	wg.Wait()
	close(errors)
	if err, ok := <-errors; ok {
		return err
	}
	return nil
}

func (c Client) Close() error {
	if c.conn == nil {
		return nil
	}
	return c.conn.Close()
}
