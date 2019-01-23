#!/bin/sh

version="$1"
versioned_release_branch="release-${version}"
versioned_release_tag="v${version}"

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
git checkout -b "${versioned_release_branch}"

# 3. Commit the version update.
git add VERSION
git commit -m "Update version to ${version}."

################################
# Push branch and tag to GitHub
################################
# 1. Tag the commit to a specific version, e.g. `v0.3.1`. 
git tag "${versioned_release_tag}"

# 2. Push release branch to GitHub.
git push https://github.com/Stackdriver/stackdriver-prometheus-sidecar.git "${versioned_release_branch}"

# 3. Push tag to GitHub.
git push https://github.com/Stackdriver/stackdriver-prometheus-sidecar.git "${versioned_release_tag}"

