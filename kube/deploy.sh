#!/bin/sh

set -e
set -u

echo "Deploy to namespace ${KUBE_NAMESPACE} for Stackdriver project ${GCP_PROJECT} (location=${GCP_REGION}, cluster=${KUBE_CLUSTER})"

envsubst < prometheus-meta.yaml > _prometheus-meta.yaml.tmp
envsubst < node-exporter.yaml > _node-exporter.yaml.tmp
envsubst < kube-state-metrics.yaml > _kube-state-metrics.yaml.tmp

kubectl apply -f _prometheus-meta.yaml.tmp --as=admin --as-group=system:masters
kubectl -n "${KUBE_NAMESPACE}" expose deployment prometheus-meta --type=LoadBalancer --name=prometheus-meta || true

kubectl apply -f _node-exporter.yaml.tmp
kubectl apply -f _kube-state-metrics.yaml.tmp --as=admin --as-group=system:masters

rm _*.tmp

