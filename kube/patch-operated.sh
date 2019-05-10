#!/bin/sh

set -e
set -u

if [  $# -lt 1 ]; then
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
