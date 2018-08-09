FROM        quay.io/prometheus/busybox:latest
LABEL maintainer "Stackdriver Engineering <engineering@stackdriver.com>"

COPY stackdriver-prometheus-sidecar         /bin/stackdriver-prometheus-sidecar

USER       nobody
EXPOSE     9091
ENTRYPOINT [ "/bin/stackdriver-prometheus-sidecar" ]
