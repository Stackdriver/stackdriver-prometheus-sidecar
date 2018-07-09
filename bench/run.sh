#!/usr/bin/env bash

pushd "$( dirname "${BASH_SOURCE[0]}" )"

go build github.com/Stackdriver/stackdriver-prometheus-sidecar/cmd/stackdriver-prometheus-sidecar

trap 'kill 0' SIGTERM

echo "Starting Prometheus"

prometheus \
  --storage.tsdb.min-block-duration=15m \
  --storage.tsdb.retention=48h 2>&1 | sed -e "s/^/[prometheus] /" &

echo "Starting server"

go run main.go 2>&1 | sed -e "s/^/[server] /" &

sleep 2
echo "Starting sidecar"

./stackdriver-prometheus-sidecar \
  --log.level=debug \
  --stackdriver.project-id=test \
  --web.listen-address="0.0.0.0:9091" \
  --stackdriver.global-label=_debug=debug \
  --stackdriver.global-label=_kubernetes_cluster_name=prom-test-cluster-1 \
  --stackdriver.global-label=_kubernetes_location=us-central1-a \
  --stackdriver.global-label=__meta_kubernetes_namespace=stackdriver \
  --stackdriver.global-label="__meta_kubernetes_pod_name=pod1" \
  --stackdriver.api-address="http://127.0.0.1:9092/?auth=false" \
  2>&1 | sed -e "s/^/[sidecar] /" &

if [ -n "${SIDECAR_OLD}" ]; then
  echo "Starting old sidecar"
  
  ${SIDECAR_OLD} \
    --stackdriver.project-id=test \
    --web.listen-address="0.0.0.0:9093" \
    --stackdriver.global-label=_kubernetes_cluster_name=prom-test-cluster-1 \
    --stackdriver.global-label=_kubernetes_location=us-central1-a \
    --stackdriver.global-label=__meta_kubernetes_namespace=stackdriver \
    --stackdriver.global-label="__meta_kubernetes_pod_name=pod1" \
    --stackdriver.api-address="http://127.0.0.1:9092/?auth=false" \
    2>&1 | sed -e "s/^/[sidecar-old] /" &
fi

wait

popd
