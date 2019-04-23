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

package file

import (
	"io/ioutil"
	"os"
	"reflect"
	"testing"

	"github.com/go-kit/kit/log"
	"github.com/golang/protobuf/proto"
	monitoring "google.golang.org/genproto/googleapis/monitoring/v3"
)

func TestRequest(t *testing.T) {
	f := NewFileClient(log.NewNopLogger())
	req := &monitoring.CreateTimeSeriesRequest{
		TimeSeries: []*monitoring.TimeSeries{
			&monitoring.TimeSeries{},
		},
	}
	if err := f.Store(req); err != nil {
		t.Fatal(err)
	}
	filename := f.file.Name()
	defer os.Remove(filename)
	f.file.Close()

	content, err := ioutil.ReadFile(filename)
	if err != nil {
		t.Fatal(err)
	}
	storedReq := &monitoring.CreateTimeSeriesRequest{}
	err = proto.Unmarshal(content, storedReq)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(req, storedReq) {
		t.Fatalf("Expect requests as %v, but stored as: %v", req, storedReq)
	}
}
