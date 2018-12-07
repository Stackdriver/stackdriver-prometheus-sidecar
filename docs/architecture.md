# Architecture

The sidecar integrates with Prometheus by following it's write-ahead-log and
combining sample information gathered from it with metric and target metadata
from the Prometheus API.
This approach was chosen over Prometheus's existing remote-write API for several
reasons:

* Stackdriver's data model is at a higher abstraction level than Prometheus's.
Thus more the remote-write API alone does not provide enough information for
the integration.
* The remote-write API only has a minimal in-memory buffer to handle intermittent
connectivey failures. The write-ahead-log on the other hand, allows us to gracefully
backfill hour-long gaps.
* The remote-write API has historically been cause for crashes and performance
issues in Prometheus. We aim for the sidecar to prevent such interference by
running in a separate process.

## Setup

Prometheus and the sidecar run alongside each other as separete containers in the
same pod. They share access to the same storage volume. Additionally, the sidecar
has access to Prometheus's target and target metadata APIs.

## Data flow

### Reading samples

Prometheus writes segments of write ahead logs to its data directory. On startup,
the sidecar starts reading the most recent WAL checkpoint, which includes records
that are required to make sense of all subsequent segment records.

This logic is implemented in package [tail](../tail).

### Converting samples

The series data alone is lacking information to convert it to Stackdriver's data
model. In Stackdriver, each series is associated with a resource (e.g. a Kubernetes
container) and has type and help information.

#### Metric metadata

The sidecar's [metadata package](../metadata), implements a cache that provides
metric metadata for a combination of metric name and `job` and `instance` labels
of a series.
It attempts to fetch metric metadata for an entire target at once to minimize
the number of requests to the Prometheus API.
The `job` and `instance` labels are generally available on all directly collected
series in Prometheus.

#### Target metadata

A similar caching layer for target information is implemented in
[package targets](../targets), which derives the original Prometheus target from
a series' labels. This allows the sidecar to learn about the full service discovery
information Prometheus originally had for this target.

The [retrieval package](../retrieval) has a hard-coded mapping from discovery
information to Stackdriver monitored resources. Fields that are not provided
through Prometheus's target metadata (e.g. Stackdriver project ID), are injected
as configuration parameters of the sidecar.

#### Building Stackdriver samples

[Package retrieval](../retrieval) provides a builder is fed Prometheus samples
from the write-ahead-log and and retrieves metadata from the caches.
It then builds full Stackdriver samples from the combined information. As the
conversion is expensive, another cache is implemented, which stores the mapping
from Prometheues's series IDs to the finalized Stackdriver metric descriptor.

The full conversion logic handles a variety of edge cases, that are best studied
in the implementation.

To avoid re-sending a large amount of data from the WAL that has been sent before,
the offset into the WAL up to which data was successfully sent, is periodically
saved to disk. On restart, the sidecar will still scan through the full WAL but only
starts sending samples after that save point.

### Sending data

The finalized Stackdriver samples are handed over to a queue in
[package stackdriver](../stackdriver), which maintains multiple active
connections to the Stackdriver API to provide sufficient throughput.
Based on how far behind the sidecar is in reading Prometheus's WAL, the number of
connections is dynamically scaled up or down.