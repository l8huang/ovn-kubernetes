# OKEP-6534: Distributed Tracing Support for OVN-Kubernetes

* Issue: [#6534](https://github.com/ovn-kubernetes/ovn-kubernetes/issues/6534)

## Problem Statement

OVN-Kubernetes currently does not provide tracing for the operations it performs. This limits
visibility into how OVN-Kubernetes handles a resource after it receives a Kubernetes event.

Pod creation is the first path to trace because it crosses several asynchronous Kubernetes and
OVN-Kubernetes components. When a pod is slow to become ready, or fails before networking is
complete, operators can usually inspect events, logs, and metrics from individual components, but
they cannot follow the same pod networking operation through OVN-Kubernetes as a trace.

The initial tracing work should therefore focus on the OVN-Kubernetes operations that are directly
part of pod creation:

* allocating pod IPs and producing pod network annotations
* programming OVN logical resources for the pod
* applying namespace, gateway, SNAT, and related pod networking state
* handling CNI ADD on the node and configuring the pod interface

## Goals

* Add opt-in distributed tracing support to OVN-Kubernetes, gated by a feature gate.
* Add initial tracing coverage for pod creation networking operations, including pod IP
  allocation, OVN logical resource programming, and pod interface configuration.
* Support propagated trace context from Kubernetes object annotations so OVN-Kubernetes spans can
  be part of a end-to-end trace.
* Make tracing configuration centrally manageable via `ovnkube-config` ConfigMap.
* Make propagated context annotation key configurable, defaulting to
  `tracing.k8s.io/traceparent`.

## Non-Goals

* Full tracing coverage for all OVN-Kubernetes resources in this initial implementation.
* Initial tracing support for NAD lifecycle operations.
* Defining backend-specific visualization requirements (Grafana/Tempo/Jaeger behavior).
* Replacing existing logs, events, or metrics.

## Introduction

Tracing support should be added as a general OVN-Kubernetes observability capability, not as a
one-off debug path for a single code branch. The initial implementation focuses on pod creation
because that is the most visible workflow where OVN-Kubernetes latency or failures directly affect
users.

Kubernetes components may propagate trace context through object annotations for asynchronous
workflows. OVN-Kubernetes should consume that context when it is present so its spans can continue
an end-to-end trace that includes components such as the API server, scheduler, and kubelet.

The design is intentionally incremental:

* provide immediate operational value for pod networking troubleshooting
* minimize risk by keeping tracing behind a feature gate
* establish reusable tracing primitives and configuration for future expansion

## User-Stories/Use-Cases

### Story 1: Troubleshoot Pod Network Bring-Up

As a cluster operator, I want spans for OVN-Kubernetes pod creation work so I can identify whether
latency or failure happened during IP allocation, OVN logical programming, or CNI interface setup.

### Story 2: End-to-End Trace a Pod Across Kubernetes Components

As a cluster operator, I want OVN-Kubernetes spans to continue propagated trace context from
Kubernetes components so I can inspect one pod creation flow across the API server, scheduler,
kubelet, and OVN-Kubernetes.

### Story 3: Enable Tracing Deliberately

As a platform administrator, I want tracing to be disabled by default and configured in
`ovnkube-config` so I can enable it only in clusters where a collector and sampling policy are
ready.

## Proposed Solution

This proposal adds an opt-in OpenTelemetry based tracing path to OVN-Kubernetes. When enabled,
ovnkube-controller and ovnkube-node emit spans for selected pod creation operations and export
them to the configured OTLP endpoint.

The implementation uses `ovnkube-config` for tracing settings and reads propagated trace context
from Pod annotations when available. Invalid or missing propagated context does not affect pod
networking; OVN-Kubernetes falls back to starting a new trace.

### API Details

No Kubernetes CRD API schema changes are required for this initial feature.

Configuration is introduced through OVN-Kubernetes existing config surfaces:

* feature gate flagging
* `ovnkube-config` ConfigMap

### Feature Gate

Tracing is gated by a new OVN-Kubernetes feature flag:

* `enable-tracing=false`: existing behavior; no tracing exporter initialization, no spans
  emitted.
* `enable-tracing=true`: tracing initialization and span emission enabled, subject to runtime
  config validity.

In `ovnkube-config`, this is configured in the existing feature section:

```ini
[ovnkubernetesfeature]
enable-tracing=true
```

### Tracing Configuration (`ovnkube-config`)

Add a `[tracing]` section under `ovnkube-config` for OpenTelemetry exporter and runtime
settings.

```ini
[tracing]
endpoint=127.0.0.1:4317
insecure=true
service-name=ovn-kubernetes
sampling-rate=1.0
propagated-context-annotation-key=tracing.k8s.io/traceparent
export-timeout=10
batch-timeout=5
max-export-batch-size=512
max-queue-size=2048
```

Initial fields:

* `endpoint`: OTLP gRPC endpoint for the exporter. Required when tracing is enabled.
* `insecure`: boolean for OTLP transport mode.
* `service-name`: OpenTelemetry `service.name`; defaults to `ovn-kubernetes`.
* `sampling-rate`: root trace sampling ratio in range `[0.0, 1.0]`.
* `propagated-context-annotation-key`: Pod annotation key used for propagated context.
* `export-timeout`: exporter timeout, in seconds.
* `batch-timeout`: batch span processor timeout, in seconds.
* `max-export-batch-size`: maximum number of spans per export batch.
* `max-queue-size`: maximum queued spans before export.

Validation and fallback:

* If tracing is enabled and `endpoint` is empty, configuration is rejected.
* `sampling-rate` must be in range `[0.0, 1.0]`.
* Missing optional values fall back to defaults.
* Missing annotation key config falls back to `tracing.k8s.io/traceparent`.

### Propagated Context Semantics

OVN-Kubernetes consumes propagated trace context from Pod annotations. This allows
OVN-Kubernetes spans for pod networking work to continue a trace that was started by another
component.

The default annotation key is:

* `tracing.k8s.io/traceparent`

The annotation value uses the W3C `traceparent` format:

```text
00-<32hex trace id>-<16hex parent span id>-<2hex flags>
```

When the annotation exists and is valid, OVN-Kubernetes:

1. Parse trace ID, parent span ID, and trace flags.
2. Build a remote parent `SpanContext`.
3. Start the OVN-Kubernetes operation span as a child of that context.

The resulting OVN-Kubernetes span has:

* the same trace ID as the annotation
* the annotation span ID as its parent span ID
* a new local span ID generated by OVN-Kubernetes

When the annotation is missing or invalid, OVN-Kubernetes does not fail pod processing. It
starts a normal new trace for the operation.

This OKEP intentionally uses parent-child continuation for the pod creation path when valid
propagated context is present. It does not use span links for this initial behavior, because the
goal is to provide one trace view across Kubernetes and OVN-Kubernetes components for the targeted
pod workflow.

### Initial Instrumentation Scope

Initial instrumentation is limited to pod networking creation/update critical path:

* ovnkube-controller pod add/update handling entry spans
* logical port programming sub-operations
* namespace/address set related pod network setup
* gateway/SNAT routing operations for pod
* NBDB transaction operation for pod programming
* ovnkube-node CNI ADD/DEL handling spans and key sub-operations

Tracing is intended for new source events that represent active pod lifecycle work, such as pod
add, update, and delete events received by OVN-Kubernetes controllers. Startup sync over resources
that already exist in the API server is not traced as pod creation work. This avoids adding new
spans to stale propagated contexts and avoids producing misleading traces after a controller
restart.

NAD-specific lifecycle tracing is excluded from this initial OKEP.

## Implementation Details

### Components

* `pkg/tracing`:
  * tracer provider initialization/shutdown
  * propagated context extraction helper from pod annotations
  * common attribute helpers
* ovnkube-cluster-manager pod path:
  * start pod add/delete handling spans for cluster-wide pod networking decisions
  * emit child spans for pod IP allocation on layer2 secondary networks
  * do not emit per-pod spans while syncing already-existing pods during startup
* ovnkube-controller pod path:
  * start top-level span for pod add/update resource handling
  * pass context through nested pod handling functions
  * emit child spans for major sub-operations
* ovnkube-node/CNI path:
  * start CNI command spans
  * use pod annotation as remote parent when available
  * emit child spans for execution and marshal/transact steps

### Context Propagation Flow (Initial)

For asynchronous pod creation flows, an upstream component can write trace context to the Pod
annotation configured in `ovnkube-config`. OVN-Kubernetes pod handlers read that annotation when
they process the Pod. If the value is valid, the handler starts its span with the propagated
context as the remote parent. Any nested OVN-Kubernetes work then uses the normal OpenTelemetry
context passed through the call path, so child operations remain in the same trace.

If the annotation is missing or invalid, OVN-Kubernetes does not reject or delay pod processing.
The handler starts a new trace for the local operation and continues normally.

Initial sync after controller startup is treated as controller recovery, not as a new k8s resource
operation. During this path OVN-Kubernetes reconciles already-existing objects from the API
server without emitting per-resource operation spans.

### Failure Handling

Tracing must not affect pod networking correctness. Failures in the tracing path are handled as
observability failures, not datapath failures.

* Invalid or unsupported propagated context: ignore the annotation, start a new local trace for
  that operation, and log enough detail for debugging without blocking pod processing.
* Invalid tracing configuration: reject startup when tracing is enabled and required fields are
  missing, or when values such as `sampling-rate` are invalid.
* Exporter or collector unavailable after startup: do not fail pod networking operations; rely on
  OpenTelemetry batching and export timeout behavior, and log exporter errors at an appropriate
  rate to avoid log spam.

### Performance Considerations

* Tracing is disabled by default.
* Sampling and batch settings are configurable in `ovnkube-config`.
* Production users are expected to tune sampling rates for scale.


## Testing Details

### Unit Testing

* Traceparent parser and validation tests:
  * valid format
  * invalid format
  * invalid hex fields
  * invalid lengths
* Context extraction helper tests:
  * missing annotation
  * configured key override
  * valid annotation yields remote parent context
* Span parentage tests for targeted functions:
  * valid annotation -> child span has expected trace ID and parent ID
  * missing/invalid annotation -> root span behavior

### E2E Testing

* Feature gate disabled:
  * verify no tracing initialization and no span emission expectations
* Feature gate enabled + valid OTEL endpoint:
  * create pod with propagated annotation
  * verify OVN-Kubernetes pod-path spans are exported
  * verify trace ID continuity with annotation-provided trace ID
* Feature gate enabled + invalid annotation:
  * verify operation succeeds
  * verify fallback root span behavior


## Documentation Details

* Add end-user docs for:
  * enabling `enable-tracing`
  * configuring `ovnkube-config` tracing block
  * annotation key behavior and defaults
  * troubleshooting invalid context and exporter connectivity
* Update `mkdocs.yml` to include this OKEP path.

## Risks, Known Limitations and Mitigations

Risks:

* tracing overhead under high pod churn
* misconfigured exporter causing noisy logs
* inconsistent upstream annotation injection across components

Mitigations:

* feature gate disabled by default
* configurable sampling and batching
* graceful fallback when annotation or exporter path is invalid

Known limitations in this initial version:

* initial scope is pod path only
* NAD and other resources are not covered yet
* e2e trace continuity depends on upstream component annotation propagation behavior

## OVN-Kubernetes Version Skew

Planned as an alpha/initial capability behind a feature gate in the next available release
after merge.

Skew behavior:

* upgraded components with tracing enabled can emit spans independently.
* mixed-version clusters may have partial span coverage.

## Backwards Compatibility

* Backward compatible by default (`enable-tracing=false`).
* No API breaking changes.
* No datapath behavior changes.

## Alternatives

* Hard-coded tracing config via environment variables only.
  * Not selected; ConfigMap-based configuration is more operationally manageable.
* Broad all-resource tracing in first iteration.
  * Not selected to reduce risk and scope.

## References

* [Kubernetes async context propagation KEP draft][k8s-async-trace-kep]

* OpenTelemetry Trace Context:
  * https://www.w3.org/TR/trace-context/
* OpenTelemetry tracing concepts:
  * https://opentelemetry.io/docs/concepts/signals/traces/

[k8s-async-trace-kep]: https://github.com/kubernetes/enhancements/pull/5915
