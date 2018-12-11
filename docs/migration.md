# Migration from stackdriver-prometheus

This document is for users of the previous Stackdriver Prometheus integration [strackdriver-prometheus](https://github.com/Stackdriver/stackdriver-prometheus) who wish to migrate to the integration in this repository ([stackdriver-prometheus-sidecar](https://github.com/Stackdriver/stackdriver-prometheus-sidecar/new/master/docs)). For full instruction on how to set up the sidecar, refer to the [README](README.md).

The old integration packaged some of the Prometheus Server functionality in the same binary and was configured in the Prometheus Server configuration file. The new integration runs side-by-side with an existing Prometheus Server and is configured directly.

## Required configuration

In the old configuration you had a block like:

```yaml
global:
  external_labels:
    _stackdriver_project_id: 'prometheus-to-sd'
    _kubernetes_cluster_name: 'prom-test-cluster-1'
    _kubernetes_location: 'us-central1-a'
```

In the new configuration you pass those variables as command-line flags to the sidecar:

```sh
stackdriver-prometheus-sidecar \
  --stackdriver.project-id="prometheus-to-sd" \
  --prometheus.wal-directory=${WAL_DIR}" \
  --prometheus.api-address=${API_ADDRESS} \
  --stackdriver.kubernetes.location="us-central1-a" \ 
  --stackdriver.kubernetes.cluster-name="prom-test-cluster-1"
```

## Additional configuration

In the old configuration you could filter metrics using Prometheus relabel configs such as:

```yaml
       metric_relabel_configs:
       - source_labels: [__name__]
         regex: 'go_memstats_mallocs_total'
         action: drop
```

In the new configuration you can set up filters by passing PromQL label matchers as command-line flags to the sidecar:

```sh
stackdriver-prometheus-sidecar --filter='__name__!="go_memstats_mallocs_total"'
```
