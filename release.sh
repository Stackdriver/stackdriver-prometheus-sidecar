#!/bin/sh

version="$1"

if [[ -z "${version}" ]]; then
    echo "Please provide a version: ./release.sh {VERSION}"
    echo "The current version is: $(cat VERSION)"
    exit 1
fi

SED_I="sed -i"
if [[ "$(uname -s)" == "Darwin" ]]; then
    SED_I="${SED_I} ''"
fi

################################
# Build and release docker image
################################

# 1. Update file `VERSION` with the numeric version, e.g. `0.3.1`.
echo "${version}" > VERSION

# 2. Create a git branch for the version, e.g. `release-0.3.1`.
git checkout -b "release-${version}"

# 3. Run `DOCKER_IMAGE_NAME={public_docker_image} make push`.
# TODO(jkohen): This is the public repo. It should replace the line below when we go public. 
#DOCKER_IMAGE_NAME="gcr.io/stackdriver-prometheus/stackdriver-prometheus-sidecar" make push
DOCKER_IMAGE_NAME="gcr.io/prometheus-to-sd/stackdriver-prometheus-sidecar" make push
