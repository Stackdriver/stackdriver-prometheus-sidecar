/*
Copyright 2018 Google Inc.
Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at
    http://www.apache.org/licenses/LICENSE-2.0
Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/
package main

import (
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	_ "net/http/pprof" // Comment this line to disable pprof endpoint.
	"time"

	"github.com/golang/protobuf/ptypes/empty"
	grpc_prometheus "github.com/grpc-ecosystem/go-grpc-prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"golang.org/x/net/context"
	monitoring_pb "google.golang.org/genproto/googleapis/monitoring/v3"
	"google.golang.org/grpc"
)

func main() {
	var (
		listenAddress  = flag.String("listen-address", ":9092", "The address to listen on for HTTP requests.")
		metricsFile    = flag.String("metrics-file", "node.txt", "Text file containing static metrics,")
		metricsAddress = flag.String("metrics-address", ":9192", "The address to listen on for Prometheus metrics requests.")
		backendLatency = flag.Duration("latency", 200*time.Millisecond, "The latency of the CreateTimeSeries requests as a Go duration")
	)
	flag.Parse()
	listener, err := net.Listen("tcp", *listenAddress)
	if err != nil {
		log.Fatalln("failed to listen on primary port:", err)
	}
	grpc_prometheus.EnableHandlingTimeHistogram()

	srv := grpc.NewServer(
		grpc.StreamInterceptor(grpc_prometheus.StreamServerInterceptor),
		grpc.UnaryInterceptor(grpc_prometheus.UnaryServerInterceptor),
	)
	monitoring_pb.RegisterMetricServiceServer(srv, &server{latency: *backendLatency})

	metricsText, err := ioutil.ReadFile(*metricsFile)
	if err != nil {
		log.Fatalln("failed reading metric file:", err)
	}
	grpc_prometheus.Register(srv)
	http.Handle("/metrics", promhttp.Handler())

	go func() {
		log.Fatal(http.ListenAndServe(*metricsAddress, nil))
	}()

	for i := 1; i <= 100; i++ {
		go func(i int) {
			err := http.ListenAndServe(fmt.Sprintf(":8%03d", i), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Write(metricsText)
			}))
			if err != nil {
				log.Fatalln(err)
			}
		}(i)
	}
	if err := srv.Serve(listener); err != nil {
		log.Fatalln("failed to server:", err)
	}
}

type server struct {
	monitoring_pb.MetricServiceServer
	latency time.Duration
}

func (srv *server) CreateTimeSeries(_ context.Context, req *monitoring_pb.CreateTimeSeriesRequest) (*empty.Empty, error) {
	time.Sleep(srv.latency)
	return &empty.Empty{}, nil
}
