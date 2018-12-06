# Operations

## Prerequisites

The sidecar exposes a variety of metrics about its internal state that are
essential during troubleshooting. Ensure that its associated Prometheus server is
configured to scrape the sidecar's `/metrics` endpoint.

## Verify that the sidecar is running

Verify that the sidecar is running along your Prometheus server:

```
kubectl -n <your_namespace> get pods
```

You should see the following line:

```
NAME                                 READY   STATUS    RESTARTS   AGE
...
prometheus-k8s-85cf598f75-64fjk      2/2     Running   0          24m
...
```

If it shows to have only one container (Ready: `1/1`), go back to the setup
instructions and verify that you've correctly configured the Prometheus
deployment/stateful set.

If it shows not both containers are ready, check the logs of the Prometheus and
sidecar containers for any error messages:

```
kubectl -n <your_namesapce> logs <pod_name> prometheus
kubectl -n <your_namesapce> logs <pod_name> sidecar
```

## Verify that the sidecar operates correctly

### Does the sidecar process Prometheus's data?

The sidecar follows the write-ahead-log of the Prometheus storage and converts
Prometheus data into Stackdriver time series.

Go to the Prometheus UI and run the following query:

```
rate(prometheus_sidecar_samples_processed[5m])
```

It should produce a value greater than 0, which indicates how many Prometheus
samples the sidecar is continously processing.

If it is zero, go to the `/targets` page in the UI and verify that Prometheus
itself is actually ingesting data. If no targets are visible, consult the
[Prometheus documentation][prom-getting-started] on how to configure Prometheus correctly.

### Are samples being sent to Stackdriver?

Run the following query to verify that the sidecar produces Stackdriver data
from the Prometheus samples:

```
rate(prometheus_sidecar_samples_produced[5m])
```

The number is generally expected to be lower than the number of processed samples
since multiple Prometheus samples (e.g. histogram buckets) may be consolidated
into a single complex Stackdriver sample.

If it is zero, check the sidecar's logs for reported errors.

Verify that the produced samples are successfully being sent to Stackdriver:

```
rate(prometheus_remote_storage_succeeded_samples_total[5m])
```

The number should generally match the number of produced samples from the previous
metric. If it is notably lower, check the sidecars logs for hints that Stackdriver
rejected some samples.
If no samples were sent successfully at all, the logs might indicate a broader
error such as invalid credentials.

### Can the sidecar keep up with Prometheus?

The number of samples produced by Prometheus and processed by the sidecar, should
be virtually identical. The following two queries should report nearly the same
number:

```
rate(prometheus_sidecar_samples_processed[5m])
rate(prometheus_tsdb_head_samples_appended_total[5m])
```

If the sidecar's processed samples are notably lower, Prometheus may be producing
more data than the sidecar can process and/or write to Stackdriver.
Check the sidecar for logs that indicate rate limiting by the Stackdriver API.
You can further verify backpressure with the following query:

```
prometheus_remote_storage_queue_length{queue="https://monitoring.googleapis.com:443/"} /
prometheus_remote_storage_queue_capacity{queue="https://monitoring.googleapis.com:443/"}
```

If the queue fullness has an upward trend or has already reached 1, you may
consider [filtering][filter-docs] the amount of data that is forward to
Stackdriver to excldue particularly noisy or high-volume metrics.
Reducing the overall scrape interval of Prometheus is another option.


[prom-getting-started]: https://prometheus.io/docs/prometheus/latest/getting_started/
[filter-docs]: ../README.md#filters

