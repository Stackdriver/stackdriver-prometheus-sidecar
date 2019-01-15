# Kubernetes setup

This directory contains patch scripts to inject the Prometheus sidecar into
existing Prometheus installations and to deploy a full example setup.

Required environment variables:
* `KUBE_NAMESPACE`: namespace to run the script against
* `KUBE_CLUSTER`: cluster name parameter for the sidecar
* `GCP_REGION`: GCP region parameter for the sidecar
* `GCP_PROJECT`: GCP project parameter for the sidecar
* `SIDECAR_IMAGE_TAG`: Version parameter for the sidecar

If your cluster is not the default context:
kubectl config use-context `KUBERNETES_CONTEXT`

## `patch.sh`

Inject sidecar into Deployments or StatefulSets:

```sh
./patch.sh <deployment|statefulset> <name>
```

Additional environment variables:
* `DATA_DIR`: data directory for the sidecar
* `DATA_VOLUME`: name of the volume that contains Prometheus's data

## `patch-operated.sh`

Injects sidecar into Prometheus deployments controlled by the [prometheus-operator](https://github.com/coreos/prometheus-operator):

```sh
./patch-operated.sh <prometheus_name>
```

## `full/deploy.sh`

Deploys a basic Prometheus deployment to monitor Kubernetes components and
custom services that are annotated with the well-known `prometheus.io/*` annotations.

```sh
./full/deploy.sh
```
