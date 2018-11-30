# Stackdriver Prometheus sidecar

This repository contains a sidecar for the Prometheus server that can send
metrics to Stackdriver. This is based on [this
design](https://docs.google.com/document/d/1TEqqE_Stq04drhjSU1I7Ctmuy0dpsvlPL1AKxqEQoSg/edit). Access
to the design document requires membership on
prometheus-developers@googlegroups.com.

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
  --prometheus.wal-directory=${WAL_DIR}" \
  --prometheus.api-address=${API_ADDRESS} \
  --stackdriver.kubernetes.location=${REGION} \ 
  --stackdriver.kubernetes.cluster-name=${CLUSTER}
```

The sidecar requires write access to the directory to store its progress between restarts.

If your Prometheus server itself is running inside of Kubernetes, the example manifests
can be used as a reference for setup:

* [Standard deployment](./kube/prometheus-meta.yaml)
* [Deployment with the Prometheus Operator](./kube/prometheus-meta-operated.yaml)


## Compatibility

The matrix below lists the versions of Prometheus Server and other dependencies that have been qualified to work with releases of `stackdriver-prometheus-sidecar`.

| sidecar version | **Prometheus 2.4.3** | **Prometheus 2.5.x** | **Prometheus 2.6.x** |
|------------|------------------|-------------------|-------------------|
| **0.2.x**  |        ✓         |         -         |         -         |
| **0.3.x**  |        ✓         |         -         |         ✓         |

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
