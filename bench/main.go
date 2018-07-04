package main

import (
	"fmt"
	"io/ioutil"
	"net"
	"net/http"

	"github.com/golang/protobuf/ptypes/empty"
	"golang.org/x/net/context"
	monitoring "google.golang.org/genproto/googleapis/monitoring/v3"
	"google.golang.org/grpc"
)

func main() {
	nodeout, err := ioutil.ReadFile("node.txt")
	if err != nil {
		panic(err)
	}
	for i := 1; i <= 100; i++ {
		go func(i int) {
			err := http.ListenAndServe(fmt.Sprintf(":8%03d", i), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Write(nodeout)
			}))
			if err != nil {
				panic(err)
			}
		}(i)
	}

	srv := grpc.NewServer()
	monitoring.RegisterMetricServiceServer(srv, &server{})

	l, err := net.Listen("tcp", "127.0.0.1:9092")
	if err != nil {
		panic(err)
	}
	if err := srv.Serve(l); err != nil {
		panic(err)
	}
}

type server struct {
	monitoring.MetricServiceServer
}

func (*server) CreateTimeSeries(_ context.Context, req *monitoring.CreateTimeSeriesRequest) (*empty.Empty, error) {
	return &empty.Empty{}, nil
}
