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

package file

import (
	"io"

	"github.com/go-kit/kit/log"
	"github.com/go-kit/kit/log/level"
	"github.com/golang/protobuf/proto"
	monitoring "google.golang.org/genproto/googleapis/monitoring/v3"
)

// FileClient allows writing to a file gRPC endpoint. The
// implementation may hit a single backend, so the application should create a
// number of these clients.
type FileClient struct {
	logger      log.Logger
	writeCloser io.WriteCloser
}

// NewFileClient creates a file under os.TempDir(), and creates a new FileClient writing to
// the file. The user of NewFileClient is responsible to manage the created file.
func NewFileClient(writeCloser io.WriteCloser, logger log.Logger) *FileClient {
	if logger == nil {
		logger = log.NewNopLogger()
	}
	return &FileClient{
		writeCloser: writeCloser,
		logger:      logger,
	}
}

// Store writes a batch of samples to the file.
func (fc *FileClient) Store(req *monitoring.CreateTimeSeriesRequest) error {
	data, err := proto.Marshal(req)
	if err != nil {
		level.Warn(fc.logger).Log(
			"msg", "failure marshaling CreateTimeSeriesRequest.",
			"err", err)
		return err
	}
	_, err = fc.writeCloser.Write(data)
	if err != nil {
		level.Warn(fc.logger).Log(
			"msg", "failure writing data to file.",
			"err", err)
		return err
	}
	return nil
}

func (fc *FileClient) Close() error {
	return fc.writeCloser.Close()
}
