#!/bin/sh

if [ "$I_ACCEPT_THE_BILLING_CHARGES" != "true" ]; then
    printf 'The default configuration of Stackdriver Prometheus Sidecar sends metrics at a rate\n'
    printf 'billed between $25 and $50/node/day on a cluster; approximately $1500/node/month.\n\n'

    printf "Please set the environment variable I_ACCEPT_THE_BILLING_CHARGES=true to continue\n"
    exit
fi

set -e
set -u

if [  $# -le 1 ]; then
  echo -e "Usage: $0 <prometheus_name>\n"
  exit 1
fi

kubectl -n "${KUBE_NAMESPACE}" patch prometheus "$1" --type merge --patch "
spec:
  containers:
  - name: sidecar
    image: gcr.io/stackdriver-prometheus/stackdriver-prometheus-sidecar:${SIDECAR_IMAGE_TAG}
    imagePullPolicy: Always
    args:
    - \"--stackdriver.project-id=${GCP_PROJECT}\"
    - \"--prometheus.wal-directory=/data/wal\"
    - \"--stackdriver.kubernetes.location=${GCP_REGION}\"
    - \"--stackdriver.kubernetes.cluster-name=${KUBE_CLUSTER}\"
    ports:
    - name: sidecar
      containerPort: 9091
    volumeMounts:
    - mountPath: /data
      name: prometheus-$1-db
"
