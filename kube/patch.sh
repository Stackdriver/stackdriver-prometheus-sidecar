#!/bin/sh

set -e
set -u

kubectl -n "${KUBE_NAMESPACE}" patch "$1" "$2" --type strategic --patch "
spec:
  template:
    spec:
      containers:
      - name: sidecar
        image: gcr.io/prometheus-to-sd/stackdriver-prometheus-sidecar:${SIDECAR_IMAGE_TAG}
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
