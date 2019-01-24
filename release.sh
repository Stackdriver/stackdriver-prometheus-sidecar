#!/bin/sh

set -e
set -x
set -o pipefail

# Git commit from which the image was built.
COMMIT_HASH="$1"
# Hash of the image to be released.
IMAGE_HASH="$2"
# The targeting release version.
RELEASE_VERSION="$3"

GITHUB_RELEASE_BRANCH="release-${RELEASE_VERSION}"
GIT_TAG_VERSION="v${RELEASE_VERSION}"
STAGING_CONTAINER_REGISTRY="gcr.io/container-monitoring-storage/stackdriver-prometheus-sidecar"
PUBLIC_CONTAINER_REGISTRY="gcr.io/stackdriver-prometheus/stackdriver-prometheus-sidecar"

if [[ -z "${COMMIT_HASH}" ]]; then
    echo "Please provide a COMMIT_HASH: ./release.sh {COMMIT_HASH} {IMAGE_HASH} {RELEASE_VERSION}"
    echo "The current commit hash is: $(git log -n 1 --pretty=format:'%h')"
    exit 1
fi
if [[ -z "${IMAGE_HASH}" ]]; then
    echo "Please provide a IMAGE_HASH: ./release.sh {COMMIT_HASH} {IMAGE_HASH} {RELEASE_VERSION}"
    exit 1
fi
if [[ -z "${RELEASE_VERSION}" ]]; then
    echo "Please provide a RELEASE_VERSION: ./release.sh {COMMIT_HASH} {IMAGE_HASH} {RELEASE_VERSION}"
    echo "The current version is: $(cat VERSION)"
    exit 1
fi

#####################
# Publish the image #
#####################
docker pull "${STAGING_CONTAINER_REGISTRY}:${IMAGE_HASH}"
docker tag "${STAGING_CONTAINER_REGISTRY}:${IMAGE_HASH}" "${PUBLIC_CONTAINER_REGISTRY}:${RELEASE_VERSION}"
docker push "${PUBLIC_CONTAINER_REGISTRY}:${RELEASE_VERSION}"

######################################################
# Tag, create release branch and update VERSION file #
######################################################

# Check out the commit hash from where the image is built.
git checkout "${COMMIT_HASH}"

# Create a git branch for the version, e.g. `release-0.3.1`.
git checkout -b "${GITHUB_RELEASE_BRANCH}"

# Update file `VERSION` with the numeric version, e.g. `0.3.1`.
echo "${RELEASE_VERSION}" > VERSION
git add VERSION
git commit -m "Update version to ${RELEASE_VERSION}."

# Tag the commit to a specific version, e.g. `v0.3.1`.
git tag "${GIT_TAG_VERSION}"

# Push release branch to GitHub.
git push https://github.com/Stackdriver/stackdriver-prometheus-sidecar.git "${GITHUB_RELEASE_BRANCH}"

# Push tag to GitHub.
git push https://github.com/Stackdriver/stackdriver-prometheus-sidecar.git "${GIT_TAG_VERSION}"


