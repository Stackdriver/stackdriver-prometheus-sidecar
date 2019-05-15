/*
Copyright 2019 Google Inc.

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
	"io"

	"github.com/go-kit/kit/log"
	"github.com/go-kit/kit/log/level"
	"github.com/golang/protobuf/proto"
	monitoring "google.golang.org/genproto/googleapis/monitoring/v3"
)

// CreateTimeSeriesRequestWriterCloser allows writing protobuf message
// monitoring.CreateTimeSeriesRequest as wire format into the writerCloser.
type CreateTimeSeriesRequestWriterCloser struct {
	logger      log.Logger
	writeCloser io.WriteCloser
}

func NewCreateTimeSeriesRequestWriterCloser(writeCloser io.WriteCloser, logger log.Logger) *CreateTimeSeriesRequestWriterCloser {
	if logger == nil {
		logger = log.NewNopLogger()
	}
	return &CreateTimeSeriesRequestWriterCloser{
		writeCloser: writeCloser,
		logger:      logger,
	}
}

// Store writes protobuf message monitoring.CreateTimeSeriesRequest as wire
// format into the writeCloser.
func (c *CreateTimeSeriesRequestWriterCloser) Store(req *monitoring.CreateTimeSeriesRequest) error {
	data, err := proto.Marshal(req)
	if err != nil {
		level.Warn(c.logger).Log(
			"msg", "failure marshaling CreateTimeSeriesRequest.",
			"err", err)
		return err
	}
	_, err = c.writeCloser.Write(data)
	if err != nil {
		level.Warn(c.logger).Log(
			"msg", "failure writing data to file.",
			"err", err)
		return err
	}
	return nil
}

func (c *CreateTimeSeriesRequestWriterCloser) Close() error {
	return c.writeCloser.Close()
}
