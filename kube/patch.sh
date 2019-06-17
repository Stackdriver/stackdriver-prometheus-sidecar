#!/bin/sh

set -e
set -u

usage() {
  echo -e "Usage: $0 <deployment|statefulset> <name>\n"
}

if [  $# -le 1 ]; then
  usage
  exit 1
fi

# Override to use a different Docker image name for the sidecar.
export SIDECAR_IMAGE_NAME=${SIDECAR_IMAGE_NAME:-'gcr.io/stackdriver-prometheus/stackdriver-prometheus-sidecar'}

kubectl -n "${KUBE_NAMESPACE}" patch "$1" "$2" --type strategic --patch "
spec:
  template:
    spec:
      containers:
      - name: sidecar
        image: ${SIDECAR_IMAGE_NAME}:${SIDECAR_IMAGE_TAG}
        imagePullPolicy: Always
        args:
        - \"--stackdriver.project-id=${GCP_PROJECT}\"
        - \"--prometheus.wal-directory=${DATA_DIR}/wal\"
        - \"--stackdriver.kubernetes.location=${GCP_REGION}\"
        - \"--stackdriver.kubernetes.cluster-name=${KUBE_CLUSTER}\"
        #- \"--stackdriver.generic.location=${GCP_REGION}\"
        #- \"--stackdriver.generic.namespace=${KUBE_CLUSTER}\"
        ports:
        - name: sidecar
          containerPort: 9091
        volumeMounts:
        - name: ${DATA_VOLUME}
          mountPath: ${DATA_DIR}
"
