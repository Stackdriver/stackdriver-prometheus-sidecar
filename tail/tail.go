// Copyright 2018 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     https://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package tail

import (
	"bytes"
	"context"
	"io"
	"io/ioutil"
	"path/filepath"
	"strconv"
	"time"

	"github.com/pkg/errors"
	"github.com/prometheus/tsdb"
	"github.com/prometheus/tsdb/fileutil"
	"github.com/prometheus/tsdb/wal"
)

// Tailer tails a write ahead log in a given directory.
type Tailer struct {
	ctx         context.Context
	dir         string
	cur         io.ReadCloser
	nextSegment int
}

// Tail the prommetheus/tsdb write ahead log in the given directory. Checkpoints
// are read before reading any WAL segments.
// Tailing may fail if we are racing with the DB itself in deleting obsolete checkpoints
// and segments. The caller should implement relevant logic to retry in those cases.
func Tail(ctx context.Context, dir string) (*Tailer, error) {
	t := &Tailer{
		ctx: ctx,
		dir: dir,
	}
	cpdir, k, err := tsdb.LastCheckpoint(dir)
	if err == tsdb.ErrNotFound {
		t.cur = ioutil.NopCloser(bytes.NewReader(nil))
		t.nextSegment = 0
	} else {
		if err != nil {
			return nil, errors.Wrap(err, "retrieve last checkpoint")
		}
		// Open the entire checkpoint first. It has to be consumed before
		// the tailer proceeds to any segments.
		t.cur, err = wal.NewSegmentsReader(cpdir)
		if err != nil {
			return nil, errors.Wrap(err, "open checkpoint")
		}
		t.nextSegment = k + 1
	}
	return t, nil
}

// Close all underlying resources of the tailer.
func (t *Tailer) Close() error {
	return t.cur.Close()
}

// CurrentSegment returns the index of the currently read segment.
// If no successful read has been performed yet, it may be negative.
func (t *Tailer) CurrentSegment() int {
	return t.nextSegment - 1
}

func (t *Tailer) Read(b []byte) (int, error) {
	const maxBackoff = 3 * time.Second
	backoff := 10 * time.Millisecond

	for {
		n, err := t.cur.Read(b)
		if err != io.EOF {
			return n, err
		}
		select {
		case <-t.ctx.Done():
			// We return EOF here. This will make the WAL reader identify a corruption
			// if we terminate mid stream. But at least we have a clean shutdown if we
			// realy read till the end of a stopped WAL.
			return n, io.EOF
		default:
		}
		// Check if the next segment already exists. Then the current
		// one is really done.
		// We could do something more sophisticated to save syscalls, but this
		// seems fine for the expected throughput (<5MB/s).
		next, err := openSegment(t.dir, t.nextSegment)
		if err == tsdb.ErrNotFound {
			// Next segment doesn't exist yet. We'll probably just have to
			// wait for more data to be written.
			select {
			case <-time.After(backoff):
			case <-t.ctx.Done():
				return n, io.EOF
			}
			if backoff *= 2; backoff > maxBackoff {
				backoff = maxBackoff
			}
			continue
		} else if err != nil {
			return n, errors.Wrap(err, "open next segment")
		}
		t.cur = next
		t.nextSegment++
	}
}

func openSegment(dir string, n int) (io.ReadCloser, error) {
	files, err := fileutil.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	for _, fn := range files {
		k, err := strconv.Atoi(fn)
		if err != nil || k < n {
			continue
		}
		if k > n {
			return nil, errors.Errorf("next segment %d too high, expected %d", n, k)
		}
		return wal.OpenReadSegment(filepath.Join(dir, fn))
	}
	return nil, tsdb.ErrNotFound
}
