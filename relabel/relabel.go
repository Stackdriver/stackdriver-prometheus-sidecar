// Copyright 2015 The Prometheus Authors
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

package relabel

import (
	"crypto/md5"
	"fmt"
	"strings"

	"github.com/gogo/protobuf/proto"
	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/model"
	"github.com/prometheus/prometheus/config"
)

type LabelPairs []*dto.LabelPair

// Process returns the relabeled label set. The relabel configurations
// are applied in order of input.
// If a label set is dropped, nil is returned.
func Process(labels LabelPairs, cfgs ...*config.RelabelConfig) LabelPairs {
	for _, cfg := range cfgs {
		labels = relabel(labels, cfg)
		if labels == nil {
			return nil
		}
	}
	return labels
}

var emptyLabelValue = ""

func relabel(input LabelPairs, cfg *config.RelabelConfig) LabelPairs {
	lmap := make(map[string]*string)
	for i := range input {
		lmap[*input[i].Name] = input[i].Value
	}
	values := make([]string, 0, len(cfg.SourceLabels))
	for _, ln := range cfg.SourceLabels {
		lv := lmap[string(ln)]
		if lv == nil {
			lv = &emptyLabelValue
		}
		values = append(values, *lv)
	}
	val := strings.Join(values, cfg.Separator)

	switch cfg.Action {
	case config.RelabelDrop:
		if cfg.Regex.MatchString(val) {
			return nil
		}
	case config.RelabelKeep:
		if !cfg.Regex.MatchString(val) {
			return nil
		}
	case config.RelabelReplace:
		indexes := cfg.Regex.FindStringSubmatchIndex(val)
		// If there is no match no replacement must take place.
		if indexes == nil {
			break
		}
		target := string(cfg.Regex.ExpandString([]byte{}, cfg.TargetLabel, val, indexes))
		if !model.LabelName(target).IsValid() {
			delete(lmap, cfg.TargetLabel)
			break
		}
		res := cfg.Regex.ExpandString([]byte{}, cfg.Replacement, val, indexes)
		if len(res) == 0 {
			delete(lmap, cfg.TargetLabel)
			break
		}
		lmap[target] = proto.String(string(res))
	case config.RelabelHashMod:
		mod := sum64(md5.Sum([]byte(val))) % cfg.Modulus
		lmap[cfg.TargetLabel] = proto.String(fmt.Sprintf("%d", mod))
	case config.RelabelLabelMap:
		for _, l := range input {
			if cfg.Regex.MatchString(*l.Name) {
				res := cfg.Regex.ReplaceAllString(*l.Name, cfg.Replacement)
				lmap[res] = l.Value
			}
		}
	case config.RelabelLabelDrop:
		for _, l := range input {
			if cfg.Regex.MatchString(*l.Name) {
				delete(lmap, *l.Name)
			}
		}
	case config.RelabelLabelKeep:
		for _, l := range input {
			if !cfg.Regex.MatchString(*l.Name) {
				delete(lmap, *l.Name)
			}
		}
	default:
		panic(fmt.Errorf("retrieval.relabel: unknown relabel action type %q", cfg.Action))
	}

	// Reduce the input protobufs in the output to reduce allocations.
	output := make(LabelPairs, 0, len(lmap))
	for _, label := range input {
		if value, ok := lmap[*label.Name]; ok {
			label.Value = value
			delete(lmap, *label.Name)
			output = append(output, label)
		}
	}
	for k, v := range lmap {
		output = append(output, &dto.LabelPair{Name: proto.String(k), Value: v})
	}
	return output
}

// sum64 sums the md5 hash to an uint64.
func sum64(hash [md5.Size]byte) uint64 {
	var s uint64

	for i, b := range hash {
		shift := uint64((md5.Size - i - 1) * 8)

		s |= uint64(b) << shift
	}
	return s
}
