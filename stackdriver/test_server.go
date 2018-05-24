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
package stackdriver

import (
	empty_pb "github.com/golang/protobuf/ptypes/empty"
	context "golang.org/x/net/context"
	metric_pb "google.golang.org/genproto/googleapis/api/metric"
	monitoredres_pb "google.golang.org/genproto/googleapis/api/monitoredres"
	monitoring_pb "google.golang.org/genproto/googleapis/monitoring/v3"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type metricServiceServer struct {
	status *status.Status
}

func (s *metricServiceServer) ListMonitoredResourceDescriptors(context.Context, *monitoring_pb.ListMonitoredResourceDescriptorsRequest) (*monitoring_pb.ListMonitoredResourceDescriptorsResponse, error) {
	return nil, status.Error(codes.Unimplemented, "")
}

func (s *metricServiceServer) GetMonitoredResourceDescriptor(context.Context, *monitoring_pb.GetMonitoredResourceDescriptorRequest) (*monitoredres_pb.MonitoredResourceDescriptor, error) {
	return nil, status.Error(codes.Unimplemented, "")
}

func (s *metricServiceServer) ListMetricDescriptors(context.Context, *monitoring_pb.ListMetricDescriptorsRequest) (*monitoring_pb.ListMetricDescriptorsResponse, error) {
	return nil, status.Error(codes.Unimplemented, "")
}

func (s *metricServiceServer) GetMetricDescriptor(context.Context, *monitoring_pb.GetMetricDescriptorRequest) (*metric_pb.MetricDescriptor, error) {
	return nil, status.Error(codes.Unimplemented, "")
}

func (s *metricServiceServer) CreateMetricDescriptor(context.Context, *monitoring_pb.CreateMetricDescriptorRequest) (*metric_pb.MetricDescriptor, error) {
	return nil, status.Error(codes.Unimplemented, "")
}

func (s *metricServiceServer) DeleteMetricDescriptor(context.Context, *monitoring_pb.DeleteMetricDescriptorRequest) (*empty_pb.Empty, error) {
	return nil, status.Error(codes.Unimplemented, "")
}

func (s *metricServiceServer) ListTimeSeries(context.Context, *monitoring_pb.ListTimeSeriesRequest) (*monitoring_pb.ListTimeSeriesResponse, error) {
	return nil, status.Error(codes.Unimplemented, "")
}

func (s *metricServiceServer) CreateTimeSeries(context.Context, *monitoring_pb.CreateTimeSeriesRequest) (*empty_pb.Empty, error) {
	if s.status == nil {
		return &empty_pb.Empty{}, nil
	} else {
		return nil, s.status.Err()
	}
}
