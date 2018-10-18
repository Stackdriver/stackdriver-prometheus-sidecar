#!/bin/sh

set -e
set -u

# Override to use a different Docker image version for the sidecar.
export SIDECAR_IMAGE_TAG=${SIDECAR_IMAGE_TAG:-'master'}
export USE_OPERATOR=${USE_OPERATOR:-''}
export KUBE_NAMESPACE=${KUBE_NAMESPACE:-'default'}

echo "Deploy to namespace ${KUBE_NAMESPACE} for Stackdriver project ${GCP_PROJECT} (location=${GCP_REGION}, cluster=${KUBE_CLUSTER}), operator=${USE_OPERATOR}"

envsubst < prometheus-base.yaml > _prometheus-base.yaml.tmp
envsubst < prometheus-meta-operated.yaml > _prometheus-meta-operated.yaml.tmp
envsubst < prometheus-meta.yaml > _prometheus-meta.yaml.tmp
envsubst < node-exporter.yaml > _node-exporter.yaml.tmp
envsubst < kube-state-metrics.yaml > _kube-state-metrics.yaml.tmp

kubectl apply -f _prometheus-base.yaml.tmp --as=admin --as-group=system:masters

if [ -n "${USE_OPERATOR}" ]; then
  kubectl apply -f _prometheus-meta-operated.yaml.tmp
else
  kubectl apply -f _prometheus-meta.yaml.tmp
fi

kubectl apply -f _node-exporter.yaml.tmp
kubectl apply -f _kube-state-metrics.yaml.tmp --as=admin --as-group=system:masters

rm _*.tmp

