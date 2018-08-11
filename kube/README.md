# Kubernetes Test Setup

This directory contains files to deploy Prometheus with the sidecar in a Kubernetes
cluster. Additional manifests deploy the Prometheus node exporter and kube-state-metrics,
which provide a further variety of metrics.

To deploy all components:

`KUBE_NAMESPACE=sidecar-test GCP_REGION=your_region GCP_PROJECT=your_project_id KUBE_CLUSTER=clustername ./deploy.sh`

Setting `USE_OPERATOR=1` will deploy Prometheus via the [coreos/prometheus-operator](https://github.com/coreos/prometheus-operator).

To tear down everything:

`kubectl delete namespace "${KUBE_NAMESPACE}"`

