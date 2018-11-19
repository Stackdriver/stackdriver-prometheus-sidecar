# Kubernetes setup

This directory contains patch scripts to inject the Prometheus sidecar into
existing Prometheus installations

* `patch.sh`: inject sidecar into Deployments or StatefulSets
* `patch-operated.sh`: injects sidecar into Prometheus deployments controlled by the [prometheus-operator](https://github.com/coreos/prometheus-operator)
* `full/deploy.sh`: deploys a full basic Prometheus deployment to monitor
Kubernetes components and custom services that are annotated with the well-known
`prometheus.io/*` annotations.

Required environment variables:
* `KUBE_NAMESPACE`: namespace to run the script against
* `KUBE_CLUSTER`: cluster name parameter for the sidecar
* `GCP_REGION`: GCP region parameter for the sidecar
* `GCP_PROJECT`: GCP project parameter for the sidecar
* `DATA_DIR`: data directory for the sidecar (`patch.sh` only)
* `DATA_VOLUME`: name of the volume that contains Prometheus's data (`patch.sh` only)

