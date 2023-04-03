ARG DOCKER_IMAGE_BASE=gcr.io/distroless/static:latest
FROM  $DOCKER_IMAGE_BASE
LABEL maintainer "Stackdriver Engineering <engineering@stackdriver.com>"

COPY stackdriver-prometheus-sidecar         /bin/stackdriver-prometheus-sidecar

EXPOSE     9091
ENTRYPOINT [ "/bin/stackdriver-prometheus-sidecar" ]
