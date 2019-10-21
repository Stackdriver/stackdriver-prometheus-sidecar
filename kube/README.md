# Kubernetes setup

This directory contains patch scripts to configure the Stackdriver Prometheus integration to work with the Prometheus server. See the [user documentation](https://cloud.google.com/monitoring/kubernetes-engine/prometheus) for more details.

Required environment variables:
* `KUBE_NAMESPACE`: namespace to run the script against
* `KUBE_CLUSTER`: cluster name parameter for the sidecar
* `GCP_REGION`: GCP region parameter for the sidecar
* `GCP_PROJECT`: GCP project parameter for the sidecar
* `SIDECAR_IMAGE_TAG`: Version parameter for the sidecar

Optional environment variables:
* `SIDECAR_IMAGE_NAME`: Image name parameter for the sidecar (default:
  gcr.io/stackdriver-prometheus/stackdriver-prometheus-sidecar)

If your cluster is not the default context:

```sh
kubectl config use-context <kubernetes_context>
```

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

Deploys a basic Prometheus deployment that generates metric data useful for testing. This setup may incur charges and isn't intended for use in production.

```sh
./full/deploy.sh
```
