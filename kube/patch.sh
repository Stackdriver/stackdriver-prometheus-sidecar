#!/bin/sh

set -e
set -u

CLEAN_UP_ORPHANED_REPLICA_SETS='--clean-up-orphaned-replica-sets'
usage() {
  echo -e "Usage: $0 <deployment|statefulset> <name> [${CLEAN_UP_ORPHANED_REPLICA_SETS}]\n"
}

if [  $# -le 1 ]; then
  usage
  exit 1
fi

SHOULD_CLEAN_UP=${3:-}

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
if [[ "${SHOULD_CLEAN_UP}" == "${CLEAN_UP_ORPHANED_REPLICA_SETS}" ]]; then
  # Delete the replica sets from the old deployment. If the Prometheus Server is
  # a deployment that does not have `revisionHistoryLimit` set to 0, this is
  # useful to avoid PVC conflicts between the old replica set and the new one
  # that prevents the pod from entering a RUNNING state.
  kubectl -n "${KUBE_NAMESPACE}" get rs | grep "$2-" | awk '{print $1}' | xargs kubectl delete -n "${KUBE_NAMESPACE}" rs
fi
