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

package targets

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/go-kit/kit/log"
	"github.com/go-kit/kit/log/level"
	"github.com/pkg/errors"
	"github.com/prometheus/prometheus/pkg/labels"
)

const DefaultAPIEndpoint = "api/v1/targets"

func cacheKey(job, instance string) string {
	return job + "\xff" + instance
}

// Cache retrieves target information from the Prometheus API and caches it.
// It works reliably and efficiently for configurations where targets are identified
// unique by job and instance label and an optional but consistent set of additional labels.
// It only provides best effort matching for configurations where targets are identified
// by a varying set of labels within a job and instance combination.
// Implements TargetGetter.
type Cache struct {
	logger log.Logger
	client *http.Client
	url    *url.URL

	mtx sync.RWMutex
	// Targets indexed by job/instance combination.
	targets map[string][]*Target
}

func NewCache(logger log.Logger, client *http.Client, promURL *url.URL) *Cache {
	if client == nil {
		client = http.DefaultClient
	}
	if logger == nil {
		logger = log.NewNopLogger()
	}
	return &Cache{
		logger:  logger,
		client:  client,
		url:     promURL,
		targets: map[string][]*Target{},
	}
}

// Run background refreshing of the cache.
func (c *Cache) Run(ctx context.Context) {
	tick := time.NewTicker(3 * time.Minute)
	defer tick.Stop()

	for {
		select {
		case <-tick.C:
			if err := c.refresh(ctx); err != nil {
				level.Error(c.logger).Log("msg", "refresh failed", "err", err)
			}
		case <-ctx.Done():
			return
		}
	}
}

func (c *Cache) refresh(ctx context.Context) error {
	req, err := http.NewRequest("GET", c.url.String(), nil)
	if err != nil {
		return err
	}
	resp, err := c.client.Do(req.WithContext(ctx))
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode/100 != 2 {
		return errors.Errorf("unexpected response status: %s", resp.Status)
	}
	var apiResp apiResponse
	if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
		return errors.Wrap(err, "decode response")
	}
	if apiResp.Status != "success" {
		return errors.Wrap(errors.New(apiResp.Error), "request failed")
	}

	repl := make(map[string][]*Target, len(c.targets))

	c.mtx.Lock()
	defer c.mtx.Unlock()

	for _, target := range apiResp.Data.ActiveTargets {
		key := cacheKey(target.Labels.Get("job"), target.Labels.Get("instance"))

		// If the exact target already exists, reuse the same memory object.
		for _, prev := range c.targets[key] {
			if labelsEqual(target.Labels, prev.Labels) {
				target = prev
				break
			}
		}
		repl[key] = append(repl[key], target)
	}

	// If negative lookups still cannot be found in retrieved response,
	// the negative lookups should be carried over to the new cache, so when
	// Get is called, it won't aggressively call refresh() again.
	for key, target := range c.targets {
		if target != nil {
			continue
		}
		if _, ok := repl[key]; !ok {
			repl[key] = nil
		}
	}

	c.targets = repl

	return nil
}

// Get returns a target that matches the job/instance combination of the input labels
// and has the same labels values for other labels keys they have in common.
// If multiple targets satisfy this property, it is undefined which one is returned. This
// is generally a non-issue if the additional label keys are consistently set across all targets
// for the job/instance combination.
// If no target is found, nil is returned.
func (c *Cache) Get(ctx context.Context, lset labels.Labels) (*Target, error) {
	key := cacheKey(lset.Get("job"), lset.Get("instance"))

	c.mtx.RLock()
	defer c.mtx.RUnlock()

	ts, ok := c.targets[key]
	// On the first miss, we enforce an immediate refresh. If there are no targets found,
	// we still set an empty entry.
	// This means for subsequent gets against the job/instance pair, requests will return
	// no targets until a refresh triggered by another lookup or the background routine populates it.
	if !ok {
		c.mtx.RUnlock()
		err := c.refresh(ctx)
		c.mtx.RLock()
		if err != nil {
			return nil, errors.Wrap(err, "target refresh failed")
		}
		if ts, ok = c.targets[key]; !ok {
			// Set empty value so we don't refresh next time.
			c.targets[key] = nil
			return nil, nil
		}
	}
	t, _ := targetMatch(ts, lset)
	return t, nil
}

// targetMatch returns the first target in the entry that matches all labels of the input
// set iff it has them set.
// This way metric labels are skipped while consistent target labels are considered.
func targetMatch(targets []*Target, lset labels.Labels) (*Target, bool) {
Outer:
	for _, t := range targets {
		for _, tl := range t.Labels {
			v := lset.Get(tl.Name)
			if v != "" && v != tl.Value {
				continue Outer
			}
		}
		return t, true
	}
	return nil, false
}

type apiResponse struct {
	Status string `json:"status"`
	Data   struct {
		ActiveTargets []*Target `json:"activeTargets"`
	} `json:"data"`
	Error     string `json:"error"`
	ErrorType string `json:"errorType"`
}

type Target struct {
	Labels           labels.Labels `json:"labels"`
	DiscoveredLabels labels.Labels `json:"discoveredLabels"`
}

// DropTargetLabels drops labels from the series that are found in the target with
// exact name/value matches.
func DropTargetLabels(series, target labels.Labels) labels.Labels {
	repl := series[:0]
	for _, l := range series {
		if target.Get(l.Name) != l.Value {
			repl = append(repl, l)
		}
	}
	return repl
}

func labelsEqual(a, b labels.Labels) bool {
	if len(a) != len(b) {
		return false
	}
	for i, l := range a {
		if l.Name != b[i].Name || l.Value != b[i].Value {
			return false
		}
	}
	return true
}
