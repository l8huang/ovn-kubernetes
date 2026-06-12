#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
REPO_DIR=${REPO_DIR:-/build/ovn-org/ovn-kubernetes}
KUBECONFIG=${KUBECONFIG:-${HOME}/ovn.conf}
POD_MANIFEST=${POD_MANIFEST:-${SCRIPT_DIR}/pod.httpbin.yaml}
EMITTER=${EMITTER:-${REPO_DIR}/go-controller/out/emit-trace-span}

export KUBECONFIG

next_pod_name() {
  local n=1
  while kubectl -n test get pod "httpbin-${n}" >/dev/null 2>&1; do
    n=$((n + 1))
  done
  printf 'httpbin-%d' "${n}"
}

ensure_emitter() {
  mkdir -p "$(dirname "${EMITTER}")"
  (cd "${REPO_DIR}/go-controller" && go build -mod=vendor -o "${EMITTER}" ./cmd/emit-trace-span)
}

port_open() {
  timeout 1 bash -c '</dev/tcp/127.0.0.1/4317' >/dev/null 2>&1
}

ensure_tempo_port_forward() {
  if port_open; then
    return 0
  fi

  kubectl -n tracing port-forward svc/tempo 4317:4317 >/tmp/tempo-debug-port-forward.log 2>&1 &
  local pf_pid=$!
  echo "${pf_pid}" >/tmp/tempo-debug-port-forward.pid

  for _ in $(seq 1 30); do
    if port_open; then
      return 0
    fi
    if ! kill -0 "${pf_pid}" >/dev/null 2>&1; then
      cat /tmp/tempo-debug-port-forward.log >&2 || true
      return 1
    fi
    sleep 0.5
  done

  cat /tmp/tempo-debug-port-forward.log >&2 || true
  return 1
}

random_hex_id() {
  local bytes=$1
  local hex
  while true; do
    hex=$(openssl rand -hex "${bytes}")
    if [[ ! "${hex}" =~ ^0+$ ]]; then
      printf '%s' "${hex}"
      return 0
    fi
  done
}

ensure_emitter
ensure_tempo_port_forward

pod_name=$(next_pod_name)
trace_id=$(random_hex_id 16)
span_id=$(random_hex_id 8)
traceparent="00-${trace_id}-${span_id}-01"

"${EMITTER}" "${trace_id}" "${span_id}"

python3 - "${POD_MANIFEST}" "${pod_name}" "${traceparent}" <<'PY'
from pathlib import Path
import re
import sys

path = Path(sys.argv[1])
pod_name = sys.argv[2]
traceparent = sys.argv[3]
text = path.read_text()
text = re.sub(r'(?m)^  name: httpbin(?:-\d+)?$', f'  name: {pod_name}', text, count=1)
text = re.sub(r'(?m)^    app: httpbin(?:-\d+)?$', f'    app: {pod_name}', text, count=1)
text = re.sub(
    r'(?m)^    tracing\.k8s\.io/traceparent: ".*"$',
    f'    tracing.k8s.io/traceparent: "{traceparent}"',
    text,
    count=1,
)
path.write_text(text)
PY

kubectl apply -f "${POD_MANIFEST}"
cat <<EOF_OUT
pod=${pod_name}
traceparent=${traceparent}
grafana traceID=${trace_id}
EOF_OUT
