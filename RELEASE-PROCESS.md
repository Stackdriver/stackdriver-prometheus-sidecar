# Release Process

Make sure the [end-to-end test](https://github.com/Stackdriver/stackdriver-prometheus-e2e) passes:
```sh
export KUBE_CLUSTER=integration-cluster
# Random namespace name in the form e2e-xxxxxxxx.
export KUBE_NAMESPACE="e2e-$(cat /dev/urandom | tr -dc 'a-z0-9' | fold -w 8 | head -n 1)"
export GCP_PROJECT=prometheus-to-sd
export GCP_REGION=us-central1-a

make DOCKER_IMAGE_TAG=$USER push
( cd kube/full ; SIDECAR_IMAGE_TAG=$USER ./deploy.sh )
( cd ../stackdriver-prometheus-e2e ; make )
kubectl delete namespace ${KUBE_NAMESPACE}
```

If OK, then release by running `./release.sh {VERSION}`
