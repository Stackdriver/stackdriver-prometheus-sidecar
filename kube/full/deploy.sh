#!/bin/bash

if [ "$I_ACCEPT_THE_BILLING_CHARGES" != "true" ]; then
    printf 'The default configuration of Stackdriver Prometheus Sidecar sends metrics at a rate\n'
    printf 'billed between $25 and $50/node/day on a cluster; approximately $1500/node/month.\n\n'

    printf "Please set the environment variable I_ACCEPT_THE_BILLING_CHARGES=true to continue\n"
    exit
fi

set -e
set -u

pushd "$(dirname "$0")"

# Override to use a different Docker image version for the sidecar.
export SIDECAR_IMAGE_TAG=${SIDECAR_IMAGE_TAG:-'master'}
export KUBE_NAMESPACE=${KUBE_NAMESPACE:-'default'}

echo "Deploy to namespace ${KUBE_NAMESPACE} for Stackdriver project ${GCP_PROJECT} (location=${GCP_REGION}, cluster=${KUBE_CLUSTER})"

envsubst < prometheus.yaml > _prometheus.yaml.tmp
envsubst < node-exporter.yaml > _node-exporter.yaml.tmp
envsubst < kube-state-metrics.yaml > _kube-state-metrics.yaml.tmp

kubectl apply -f _prometheus.yaml.tmp --as=admin --as-group=system:masters
kubectl apply -f _node-exporter.yaml.tmp
kubectl apply -f _kube-state-metrics.yaml.tmp --as=admin --as-group=system:masters

DATA_DIR=/data DATA_VOLUME=data-volume ../patch.sh deploy prometheus-k8s

rm _*.tmp
popd
