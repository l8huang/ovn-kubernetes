# Plan: Enable Kubernetes 1.34 API Server & Kubelet Tracing with Grafana Tempo

**TL;DR** — Enable native Kubernetes 1.34 tracing (API server + kubelet) by adding a `--enable-tracing` flag to kind-helm.sh. This will deploy Grafana Tempo + Grafana inside the cluster, configure apiserver/kubelet with OpenTelemetry exporters, and visualize traces in Grafana.

---

## Steps

### Phase 1: Kind Cluster Tracing Configuration
1. Modify Kind cluster YAML generation to enable `TracingConfig` feature gate for both apiserver and kubelet
2. Create api-server-tracing ConfigMap YAML template with sampling config (points to `otel-collector:4317`)
3. Mount ConfigMap into Kind control-plane with apiserver flag: `--tracing-config=/etc/kubernetes/tracing/config.yaml`

### Phase 2: Kubelet Tracing Configuration
1. Create kubelet-tracing-config ConfigMap (same structure as apiserver)
2. Mount into each worker node via extraMounts with kubelet flags: `--tracing-enabled=true` and `--tracing-config=/etc/kubernetes/kubelet-tracing/config.yaml`

### Phase 3: OpenTelemetry Collector Deployment
1. Deploy OTel Collector Helm chart (`open-telemetry/opentelemetry-collector-k8s`)
   - gRPC receiver on port 4317 (OTLP)
   - Tempo exporter configuration
   - Expose as ClusterIP service (`otel-collector:4317`)

### Phase 4: Grafana Tempo Installation
1. Deploy Tempo Helm chart (`grafana/tempo`)
   - Single-node setup with emptyDir storage (dev/Kind only)
   - Expose service as `tempo.tracing:3100` for datasource access

### Phase 5: Grafana Installation & Dashboard
1. Deploy Grafana Helm chart (`grafana/grafana`)
   - Pre-configure Tempo datasource (type: Tempo, URL: `http://tempo.tracing:3100`)
   - Expose via NodePort or port-forward for access

### Phase 6: kind-helm.sh Script Modifications
1. Add CLI flags: `--enable-tracing` or `-tr` (fixed at 100% sampling rate)
2. Modify `parse_args()` to capture the tracing flag
3. Modify `set_default_params()` to set defaults (ENABLE_TRACING=false, TRACING_NAMESPACE=tracing, TRACING_SAMPLING_RATE=1.0)
4. Modify `create_kind_cluster()` to inject TracingConfig feature gates and ConfigMap mounts when enabled
5. Create `create_tracing_manifests()` function to generate ConfigMaps and Helm values
6. Create `install_tracing_stack()` function to deploy Tempo, OTel Collector, and Grafana
7. Add conditional call to `install_tracing_stack()` in main script flow (after Kind creation, before OVN setup)
8. Update `print_params()` to show tracing config and Grafana access instructions

### Phase 7: Testing & Documentation
1. Add inline comments explaining tracing configuration
2. Create verification steps for traces flowing to Tempo and visible in Grafana

---

## Relevant Files
- `contrib/kind-helm.sh` — Main script; all the above modifications here
- `contrib/kind-common.sh` — Shared helpers; `create_kind_cluster()` lives here; inject feature gates & mounts
- Reference: `helm/ovn-kubernetes/` for Helm deployment patterns

---

## Verification
1. **Apiserver tracing**: `kubectl logs -n kube-system -l component=kube-apiserver | grep -i tracing`
2. **Kubelet tracing**: `docker exec <node-name> cat /var/log/kubelet.log | grep -i tracing`
3. **Traces in Tempo**: Port-forward Grafana (`kubectl port-forward -n tracing svc/grafana 3000:80`), navigate to Explore → Tempo, search for kubernetes service
4. **Helm deployments**: `helm list -n tracing` should show tempo, otel-collector, grafana releases

---

## Key Decisions
- **Namespace**: All components in `tracing` namespace for clean isolation
- **Storage**: Tempo uses `emptyDir` (dev-only; document PVC option for production)
- **Sampling**: Fixed at 1.0 (100%) — captures every request; appropriate for Kind dev/testing
- **Access**: NodePort + Grafana provisioning for pre-configured datasource/dashboards
- **OVN tracing**: Out of scope; user will add it later
- **Collector Backend**: Grafana Tempo (cloud-native, scalable)
- **Deployment Location**: Inside Kind cluster for dev/testing convenience
- **Instrumentation Scope**: API server + kubelet only (OVN tracing to be added later by user)

---

## Further Considerations

### 1. Storage Strategy
- **Current Plan**: `emptyDir` for Kind dev (traces lost on pod restart)
- **Question**: For production-like testing, should Tempo use PVC or `hostPath`?
- **Recommendation**: Keep `emptyDir` as default; document PVC option for advanced usage

### 2. Trace Retention & Cleanup
- **Current Plan**: Use Tempo's default (24 hours)
- **Note**: With 100% sampling, trace volume will be high; emptyDir storage will fill faster than at lower rates. Monitor Tempo pod disk usage during testing.

### 3. Feature Gate Stability
- **Status**: `TracingConfig` is GA in K8s 1.34 (confirmed by user)
- **Consideration**: Are there version-specific behaviors or deprecations to account for?
- **Action**: Document minimum K8s version requirement (1.34+)

### 4. Grafana Provisioning
- **Current Plan**: Pre-create datasource + dashboards via ConfigMaps (Grafana provisioning)
- **Question**: Should this be automatic, or allow manual setup?
- **Recommendation**: Auto-provision datasource; offer optional pre-built dashboards via ConfigMap

### 5. Backward Compatibility
- **Requirement**: Default `--enable-tracing=false` must not break existing kind-helm.sh workflows
- **Action**: Ensure tracing installation is fully optional; no mandatory dependencies

### 6. API Server Tracing Configuration Format
- **Format**: YAML config with serverUrl pointing to OTel Collector gRPC endpoint
- **Reference**: https://kubernetes.io/docs/concepts/cluster-administration/system-traces/
- **Example**:
  ```yaml
  apiVersion: apiserver.config.k8s.io/v1beta1
  kind: TracingConfig
  tracesSamplingRate: 1.0
  endpoint: otel-collector:4317
  ```

### 7. Kubelet Tracing Configuration
- **Kubelet Flag**: `--tracing-enabled=true`
- **Config Path**: `--tracing-config=/etc/kubernetes/kubelet-tracing/config.yaml`
- **Format**: Similar to apiserver (YAML TracingConfig)
- **Consideration**: Kubelet is on each node; ensure configurable via Kind node extraMounts

### 8. OpenTelemetry Collector Configuration
- **Receivers**: OTLP gRPC on 0.0.0.0:4317
- **Processors**: Batch processor (standard) to reduce export overhead
- **Exporters**: Tempo (gRPC) exporter to `tempo.tracing:3100` or `tempo.tracing:4317`
- **Extensions**: Health check extension for readiness/liveness probes

### 9. Helm Chart Dependencies
- **Tempo Helm Chart**: `grafana/tempo` or `grafana/tempo-distributed`
  - Use `tempo` (monolithic) for Kind simplicity
  - Single replica, emptyDir storage
- **OTel Collector Helm Chart**: `open-telemetry/opentelemetry-collector-k8s`
  - DaemonSet mode (one per node) or Deployment mode (central)
  - Note: DaemonSet not needed for tracing; use Deployment for central collection
- **Grafana Helm Chart**: `grafana/grafana`
  - Single replica
  - Pre-provisioned Tempo datasource and dashboards

### 10. Metric Endpoints & Service Discovery
- **Prometheus ServiceMonitor**: Consider whether to add Tempo metrics collection (optional)
- **Grafana Dashboard**: Should it show both traces and metrics, or traces only?
- **Recommendation**: Traces only for this phase; metric collection can be added later

### 11. Debugging & Logging
- **OTel Collector Logs**: Should be verbose during initial testing
- **Trace Verification**: Provide kubectl commands to check ConfigMaps, logs, service connectivity
- **Recommendation**: Add `--debug` or `--tracing-verbose` flag for troubleshooting

### 12. Integration with Existing Observability
- **Current Setup**: kind-helm.sh already supports `-obs` (OVN observability) and monitoring
- **Consideration**: Should tracing stack be independent, or integrated with existing observability?
- **Recommendation**: Keep independent for now; document how to use both together

---

## Implementation Priority

**High Priority** (Phase 1-5):
- Kind cluster tracing config (apiserver + kubelet feature gates)
- OTel Collector deployment
- Grafana Tempo deployment
- Basic Grafana setup with Tempo datasource

**Medium Priority** (Phase 6):
- kind-helm.sh script integration
- CLI flags and parameter handling
- Manifest generation

**Low Priority** (Phase 7):
- Advanced features (retention config, verbose logging, metric integration)
- Comprehensive dashboards
- Production-ready storage options

---

## Success Criteria

1. ✅ `kind-helm.sh --enable-tracing` successfully deploys Kind cluster with tracing enabled
2. ✅ All three components (OTel Collector, Tempo, Grafana) running in `tracing` namespace
3. ✅ API server traces visible in Grafana Trace Explorer within 30 seconds of cluster creation
4. ✅ Kubelet traces visible in Grafana Trace Explorer (sample a few worker nodes)
5. ✅ Grafana accessible at `localhost:3000` via NodePort or port-forward
6. ✅ `--help` and `kind-helm.sh -h` document new tracing flags
7. ✅ Default behavior unchanged when `--enable-tracing` is not specified
8. ✅ Sampling rate fixed at 100% — every request is traced

---

## Notes for Implementation

- Start with **Phase 1** (Kind config) and **Phase 3** (OTel Collector) in parallel—they're independent
- **Phase 2** (Kubelet config) depends on understanding Kind node configuration in kind-common.sh
- **Phase 4-5** (Tempo & Grafana) are standard Helm deployments; reference existing Helm patterns in the repo
- **Phase 6** (kind.sh mods) is the integration layer; do this after all components are working standalone
- Test after each phase to ensure traces flow end-to-end


## notes for using tracing 

kubectl port-forward -n tracing svc/grafana 3000:80 --address 0.0.0.0


kubectl create role pod-creator --verb=create --resource=pods -n test
kubectl create rolebinding default-pod-creator \
  --role=pod-creator \
  --serviceaccount=default:default \
  -n test

- required for specifying trace-id
kubectl create clusterrolebinding traceparent-test \
  --clusterrole=system:monitoring \
  --serviceaccount=test:default

kubectl auth can-i create pods \
  --as=system:serviceaccount:default:default \
  -n test


TOKEN=$(kubectl create token default)
curl -k \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/yaml" \
  -H "traceparent: 00-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-bbbbbbbbbbbbbbbb-01" \
  https://172.18.0.3:6443/api/v1/namespaces/test/pods \
  --data-binary @/build/l8huang/today/k8s/ovn-k8s/specs/udn/pod.client1.yaml


---

# Phase 8: Add Tracing in OVN-Kubernetes (Pod Creation Path)

This phase implements the OVN-Kubernetes tracing OKEP. It does not depend on the Kind tracing
setup above; the cluster tracing stack may be installed separately.

## Scope

* Add opt-in OpenTelemetry tracing to OVN-Kubernetes.
* Keep tracing disabled by default behind `OVN_ENABLE_TRACING`.
* Configure tracing through `ovnkube-config` (`ovnkube.conf`), not through hard-coded values.
* Start with pod creation networking operations only.
* Continue propagated trace context from Pod annotations when present and valid.
* Leave NAD lifecycle tracing and other resources for later phases.

## Configuration Model

Add a `[tracing]` section to `ovnkube.conf` in the `ovnkube-config` ConfigMap.

Initial config keys:

* `endpoint`: OTLP gRPC collector endpoint, for example `127.0.0.1:4317`
* `insecure`: whether to use insecure OTLP transport
* `sampling-rate`: sampling rate from `0.0` to `1.0`
* `propagated-context-annotation-key`: default `tracing.k8s.io/traceparent`
* `export-timeout`: exporter timeout
* `batch-timeout`: batch span processor timeout
* `max-export-batch-size`: maximum spans per export batch
* `max-queue-size`: maximum queued spans before dropping
* `resource-attributes`: optional static resource attributes

Example:

```ini
[tracing]
endpoint = 127.0.0.1:4317
insecure = true
sampling-rate = 1.0
propagated-context-annotation-key = tracing.k8s.io/traceparent
```

## Go Module Updates

Add or promote required OpenTelemetry modules as direct dependencies:

* `go.opentelemetry.io/otel`
* `go.opentelemetry.io/otel/trace`
* `go.opentelemetry.io/otel/sdk`
* `go.opentelemetry.io/otel/exporters/otlp/otlptrace`
* `go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc`

After implementation:

* run `go mod tidy`
* run `go mod vendor`
* verify `vendor/modules.txt` is consistent with `go.mod`

## Tracing Package

Create `go-controller/pkg/tracing` to centralize tracing behavior.

Responsibilities:

* parse tracing config from OVN-Kubernetes config
* initialize and own the OpenTelemetry `TracerProvider`
* configure OTLP gRPC exporter and batch span processor
* configure sampler from `sampling-rate`
* expose component tracers for `ovnkube-node`
* expose shutdown/flush hook for process exit
* parse W3C `traceparent` from Pod annotations
* build remote parent `SpanContext` when propagated context is valid

Invalid tracing config should disable tracing for that process and log the reason. It must not
block OVN-Kubernetes startup or pod networking operations.

## Startup and Shutdown Wiring

Initialize tracing in `cmd/ovnkube` after configuration is loaded and before controllers start.

Component resource attributes should include:

* `service.name=ovn-kubernetes`
* `service.component=ovnkube-node` 
* node name when available
* zone name when available

Shutdown should flush spans before process exit.

## Propagated Context Behavior

When processing a Pod, check the configured annotation key. The default key is
`tracing.k8s.io/traceparent`.

If the annotation exists and is valid:

* parse it as W3C traceparent: `00-<32 hex trace id>-<16 hex span id>-<2 hex flags>`
* create a remote parent `SpanContext`
* start the OVN-Kubernetes span as a child of that parent
* create child spans normally from that context

If the annotation is missing or invalid:

* start a normal new trace
* keep pod handling behavior unchanged
* log invalid context only at low verbosity

Do not use span links for this initial implementation. The OKEP intentionally chooses parent-child
continuation so operators can see OVN-Kubernetes pod creation spans in the same trace as upstream
Kubernetes spans.

## Pod Creation Spans

Start with coarse spans, then add detailed child spans once the context flow is correct.

Controller entry spans:

* `defaultNetworkControllerEventHandler.AddResource()` for Pod add
* `defaultNetworkControllerEventHandler.UpdateResource()` for Pod updates that trigger pod wiring

Pod orchestration spans:

* `ensurePod()`
* `ensureLocalZonePod()`
* `ensureRemoteZonePod()`
* `addLogicalPort()`

Logical port and IP allocation spans:

* `addLogicalPortToNetwork()`
* `allocatePodIPsOnSwitch()`
* `allocatePodAnnotation()`
* `assignPodAddresses()`
* `updatePodAnnotationWithRetry()`

Namespace, gateway, and transaction spans:

* `addLocalPodToNamespace()`
* `addGWRoutesForPod()`
* `AddPodSNATOps()`
* `addPodExternalGW()`
* NBDB transaction for pod logical resource programming

CNI spans:

* CNI ADD request handling
* CNI DEL request handling
* pod interface configure/unconfigure operations
* CNI response marshal path if useful for latency accounting

## Span Attributes

Use stable attributes that are useful for searching and grouping traces:

* `k8s.pod=<namespace>/<name>`
* `k8s.pod.uid`
* `k8s.node.name`
* `ovn.network.name`
* `ovn.nad.name`
* `ovn.zone.name`
* `ovn.pod.zone=local|remote`
* `ovn.retry=true|false`
* `cni.command=ADD|DEL`

Record errors on spans and set span status to error when an instrumented operation fails.

## Testing

Unit tests:

* config parsing and defaults for `[tracing]`
* invalid config disables tracing without failing startup
* valid `traceparent` creates a remote parent span context
* invalid/missing `traceparent` falls back to root span behavior
* span attributes include `k8s.pod=<namespace>/<name>`

Integration or e2e checks:

* `OVN_ENABLE_TRACING=false` keeps existing behavior unchanged
* tracing enabled exports spans to a reachable OTLP collector
* Pod annotation traceparent is continued by OVN-Kubernetes spans
* pod creation still succeeds when collector endpoint is unavailable

Build checks:

* `go test ./pkg/tracing ./pkg/ovn ./pkg/cni`
* `go build ./cmd/ovnkube ./cmd/ovn-k8s-cni-overlay`

## Non-Goals

* Do not trace all OVN-Kubernetes resources in this phase.
* Do not trace NAD lifecycle operations in this phase.
* Do not make the Kind tracing stack part of this phase.
* Do not require a specific tracing backend such as Tempo or Jaeger.
