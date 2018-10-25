#!/usr/bin/env bash

set -e

pushd "$( dirname "${BASH_SOURCE[0]}" )"

go build github.com/Stackdriver/stackdriver-prometheus-sidecar/cmd/stackdriver-prometheus-sidecar

trap 'kill 0' SIGTERM

echo "Starting Prometheus"

prometheus \
  --storage.tsdb.min-block-duration=15m \
  --storage.tsdb.retention=48h 2>&1 | sed -e "s/^/[prometheus] /" &

echo "Starting server"

go run main.go --latency=30ms 2>&1 | sed -e "s/^/[server] /" &

sleep 2
echo "Starting sidecar"

./stackdriver-prometheus-sidecar \
  --config-file="sidecar.yml" \
  --stackdriver.project-id=test \
  --web.listen-address="0.0.0.0:9091" \
  --stackdriver.generic.location="test-cluster" \
  --stackdriver.generic.namespace="test-namespace" \
  --stackdriver.api-address="http://127.0.0.1:9092/?auth=false" \
  2>&1 | sed -e "s/^/[sidecar] /" &

if [ -n "${SIDECAR_OLD}" ]; then
  echo "Starting old sidecar"
  
  ${SIDECAR_OLD} \
    --log.level=debug \
    --stackdriver.project-id=test \
    --web.listen-address="0.0.0.0:9093" \
    --stackdriver.debug \
    --stackdriver.api-address="http://127.0.0.1:9092/?auth=false" \
    2>&1 | sed -e "s/^/[sidecar-old] /" &
fi

wait

popd
