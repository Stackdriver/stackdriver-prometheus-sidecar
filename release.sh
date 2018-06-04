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
DOCKER_IMAGE_NAME="gcr.io/stackdriver-prometheus/stackdriver-prometheus" make push

##########################
# Deploy Prometheus server
##########################

# 1. Start from the configuration for the previous release and merge any changes from the master branch.
# NOOP

# 2. Update the version tag in the Docker image to the version you released.
/bin/sh -c "${SED_I} -E 's/(image: ).*/\1gcr.io\/stackdriver-prometheus\/stackdriver-prometheus:release-${version}/g' documentation/examples/prometheus-service.yml"

# 3. Apply it to your cluster to verify it works.
kubectl apply -f documentation/examples/prometheus-service.yml
kubectl apply -f documentation/examples/rbac-setup.yml --as=admin --as-group=system:masters
