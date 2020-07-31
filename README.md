# Stackdriver Prometheus sidecar

This repository contains a sidecar for the Prometheus server that can send
metrics to Stackdriver. This is based on [this design](docs/design.md).

Custom metrics are a chargeable feature of Stackdriver Monitoring and there could be costs for your custom metrics. For more information on pricing, see [Stackdriver Pricing](https://cloud.google.com/stackdriver/pricing).

[![Build Status](https://travis-ci.com/Stackdriver/stackdriver-prometheus-sidecar.svg?branch=master)](https://travis-ci.com/Stackdriver/stackdriver-prometheus-sidecar)
[![Coverage Status](https://coveralls.io/repos/github/Stackdriver/stackdriver-prometheus-sidecar/badge.svg?branch=master)](https://coveralls.io/github/Stackdriver/stackdriver-prometheus-sidecar?branch=master)

## Installation

To install the sidecar on a local machine run:

```
go get github.com/Stackdriver/stackdriver-prometheus-sidecar/...
```

## Deployment

The sidecar is deployed next to an already running Prometheus server. It can stream
sample data into Stackdriver that is associated with a Kubernetes target in Prometheus.

The following information must be provided:

* `GCP_PROJECT`: The ID of the GCP project the data is written to.
* `WAL_DIR`: the `./wal` directory within Prometheus's data directory.
* `API_ADDRESS`: Prometheus's API address, typically `127.0.0.1:9090` like the default
* `REGION`: a valid GCP or AWS region with which the [Stackdriver monitored resources](https://cloud.google.com/monitoring/api/resources) are associated. Maps to the resource's `location` label (sometimes also called `region`).
* `CLUSTER`: a custom name for the monitored Kubernetes cluster. Maps to the monitored resource's `cluster_name` label.

```
stackdriver-prometheus-sidecar \
  --stackdriver.project-id=${GCP_PROJECT} \
  --prometheus.wal-directory=${WAL_DIR} \
  --prometheus.api-address=${API_ADDRESS} \
  --stackdriver.kubernetes.location=${REGION} \
  --stackdriver.kubernetes.cluster-name=${CLUSTER}
```

The sidecar requires write access to the directory to store its progress between restarts.

If your Prometheus server itself is running inside of Kubernetes, the example [Kubernetes setup](./kube/README.md)
can be used as a reference for setup.

### Configuration

The majority of configuration options for the sidecar are set through flags. To see all available flags, run:

```
stackdriver-prometheus-sidecar --help
```

#### Filters

The `--include` flag allows to provide filters which all series have to pass before being sent to Stackdriver. Filters use the same syntax as [Prometheus instant vector selectors](https://prometheus.io/docs/prometheus/latest/querying/basics/#instant-vector-selectors), e.g.:

```
stackdriver-prometheus-sidecar --include='{__name__!~"cadvisor_.+",job="k8s"}' ...
```

This drops all series which do not have a `job` label `k8s` and all metrics that have a name starting with `cadvisor_`.

For equality filter on metric name you can use the simpler notation, e.g. `--include='metric_name{label="foo"}'`.

The flag may be repeated to provide several sets of filters, in which case the metric will be forwarded if it matches at least one of them. Please note that inclusion filters only apply to Prometheus metrics proxied directly, and do not apply to [aggregated counters](#counter-aggregator).

#### File

The sidecar can also be provided with a configuration file. It allows to define static metric renames and to overwrite metric metadata which is usually provided by Prometheus. A configuration file should not be required for the majority of users.

```yaml
metric_renames:
  - from: original_metric_name
    to: new_metric_name
# - ...

static_metadata:
  - metric: some_metric_name
    type: counter # or gauge/histogram
    value_type: double # or int64
    help: an arbitrary help string
# - ...
```

  * All `static_metadata` entries must have `type` specified. This specifies the Stackdriver metric type and overrides the metric type chosen by the Prometheus client.
  * If `value_type` is specified, it will override the default value type for counters and gauges. All Prometheus metrics have a default type of double.

#### Counter Aggregator

Counter Aggregator is an advanced feature of the sidecar that can be used to export a sum of multiple Prometheus counters to Stackdriver as a single CUMULATIVE metric.

You might find this useful if you have counter metrics in Prometheus with high cardinality labels (or perhaps just counters exported by a large number of targets) which makes exporting all of them to Stackdriver directly too expensive, however you would like to have a cumulative metric that has the sum of those counters.

Aggregated counters are configured in the `aggregated_counters` block of the configuration file. For example:

```yaml
aggregated_counters:
  - metric: network_transmit_bytes
    help: total number of bytes sent over eth0
    filters:
     - 'node_network_transmit_bytes_total{device="eth0"}'
     - 'node_network_transmit_bytes{device="eth0"}'
```

In this example, the sidecar will export a new counter `network_transmit_bytes`, which will correspond to the total number of bytes transmitted over 'eth0' interface across all machines monitored by Prometheus. Counter Aggregator keeps track of all counters matching the filters and correctly handles counter resets. Like all internal metrics exported by the sidecar, the aggregated counter is exported using OpenCensus and will be available in Stackdriver as a custom metric (`custom.googleapis.com/opencensus/network_transmit_bytes`).

A list of [Prometheus instant vector selectors](https://prometheus.io/docs/prometheus/latest/querying/basics/#instant-vector-selectors) is expected in the `filters` field. A time series needs to match any of the specified selectors to be included in the aggregated counter.

##### Counter aggregator and inclusion filters

Please note that by default metrics that match one of aggregated counter filters will still be exported to Stackdriver unless you have inclusion filters configured that prevent those metrics from being exported (see `--include`). Using `--include` to prevent a metric from being exported to Stackdriver does not prevent the metric from being covered by aggregated counters.

When using Counter Aggregator you would usually want to configure a restrictive inclusion filter to avoid raw metrics from being exported to Stackdriver.

## Compatibility

The matrix below lists the versions of Prometheus Server and other dependencies that have been qualified to work with releases of `stackdriver-prometheus-sidecar`. If the matrix does not list whether they are compatible, please assume they are not verified yet but can be compatible. Feel free to contribute to the matrix if you have run the end-to-end test between a version of `stackdriver-prometheus-sidecar` and Prometheus server.

| Sidecar Version | Compatible Prometheus Server Version(s)   | Incompatible Prometheus Server Version(s) |
|-----------------|-------------------------------------------|-------------------------------------------|
| **0.5.x**       | 2.6, 2.11                                 | 2.5                                       |
| **0.6.x**       | 2.11                                      | 2.5                                       |
| **0.7.x**       | 2.10, 2.11, 2.13, 2.15, 2.16, 2.18, 2.19  | 2.5                                       |
| **0.8.x**       | 2.10, 2.11, 2.13, 2.15, 2.16, 2.18, 2.19  | 2.5                                       |

## Alternatives

Google develops **stackdriver-prometheus** primarily for Stackdriver users and gives support to Stackdriver users. We designed the user experience to meet the expectations of Prometheus users and to make it easy to run with Prometheus server. stackdriver-prometheus is intended to monitor all your applications, Kubernetes and beyond.

Google develops **prometheus-to-sd** primarily for Google Kubernetes Engine to collect metrics from system services in order to support Kubernetes users. We designed the tool to be lean when deployed as a sidecar in your pod. It's intended to support only the metrics the Kubernetes team at Google needs; you can use it to monitor your applications, but the user experience could be rough.

## Source Code Headers

Every file containing source code must include copyright and license
information. This includes any JS/CSS files that you might be serving out to
browsers. (This is to help well-intentioned people avoid accidental copying that
doesn't comply with the license.)

Apache header:

    Copyright 2018 Google Inc.

    Licensed under the Apache License, Version 2.0 (the "License");
    you may not use this file except in compliance with the License.
    You may obtain a copy of the License at

        https://www.apache.org/licenses/LICENSE-2.0

    Unless required by applicable law or agreed to in writing, software
    distributed under the License is distributed on an "AS IS" BASIS,
    WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
    See the License for the specific language governing permissions and
    limitations under the License.
