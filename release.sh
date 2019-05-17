#!/bin/sh

version="$1"
versioned_release_branch="release-${version}"

if [[ -z "${version}" ]]; then
    echo "Please provide a version: ./release.sh {VERSION}"
    echo "Look up latest version on https://github.com/Stackdriver/stackdriver-prometheus-sidecar/tags."
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
#    VERSION file is required by building tool promu:
#    https://github.com/prometheus/promu
echo "${version}" > VERSION

# 2. Create a git branch for the version, e.g. `release-0.3.1`.
git checkout -b "${versioned_release_branch}"

# 3. Commit the version update.
git add VERSION
git commit -m "Update version to ${version}."

# 4. Run `DOCKER_IMAGE_NAME={public_docker_image} make push`.
DOCKER_IMAGE_NAME="gcr.io/stackdriver-prometheus/stackdriver-prometheus-sidecar" make push

################################
# Push branch and tag to GitHub
################################
# 1. Tag the commit to a specific version, e.g. `0.3.1`.
git tag "${version}"

# 2. Push release branch to GitHub.
git push https://github.com/Stackdriver/stackdriver-prometheus-sidecar.git "${versioned_release_branch}"

# 3. Push tag to GitHub.
git push https://github.com/Stackdriver/stackdriver-prometheus-sidecar.git "${version}"

