#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 1 ]]; then
  echo "usage: $0 <parent-span-id>" >&2
  exit 2
fi

SCRIPT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
KUBECONFIG=${KUBECONFIG:-${HOME}/ovn.conf}
PARENT_ID=$1
OUT_FILE="${SCRIPT_DIR}/parentID-${PARENT_ID}.json"

export KUBECONFIG

if [[ ! "${PARENT_ID}" =~ ^[0-9a-fA-F]{16}$ ]]; then
  echo "parent span ID must be 16 hex characters: ${PARENT_ID}" >&2
  exit 2
fi

port_open() {
  timeout 1 bash -c '</dev/tcp/127.0.0.1/3200' >/dev/null 2>&1
}

ensure_tempo_port_forward() {
  if port_open; then
    return 0
  fi

  kubectl -n tracing port-forward svc/tempo 3200:3200 >/tmp/tempo-query-port-forward.log 2>&1 &
  local pf_pid=$!
  echo "${pf_pid}" >/tmp/tempo-query-port-forward.pid

  for _ in $(seq 1 30); do
    if port_open; then
      return 0
    fi
    if ! kill -0 "${pf_pid}" >/dev/null 2>&1; then
      cat /tmp/tempo-query-port-forward.log >&2 || true
      return 1
    fi
    sleep 0.5
  done

  cat /tmp/tempo-query-port-forward.log >&2 || true
  return 1
}

ensure_tempo_port_forward

query="{ span:parentID = \"${PARENT_ID}\" }"
search_json=$(curl -fsS -G 'http://127.0.0.1:3200/api/search' \
  --data-urlencode "q=${query}" \
  --data-urlencode 'limit=100')

mapfile -t trace_ids < <(jq -r '.traces[]?.traceID' <<<"${search_json}" | sort -u)

if [[ ${#trace_ids[@]} -eq 0 ]]; then
  jq -n --arg parentID "${PARENT_ID}" --arg query "${query}" \
    '{parentID: $parentID, query: $query, traces: []}' > "${OUT_FILE}"
  echo "wrote ${OUT_FILE} (no matching traces)"
  exit 0
fi

tmp_dir=$(mktemp -d)
trap 'rm -rf "${tmp_dir}"' EXIT

for trace_id in "${trace_ids[@]}"; do
  curl -fsS "http://127.0.0.1:3200/api/traces/${trace_id}" \
    > "${tmp_dir}/${trace_id}.json"
done

jq -n \
  --arg parentID "${PARENT_ID}" \
  --arg query "${query}" \
  --slurpfile search <(printf '%s' "${search_json}") \
  --slurpfile traces <(jq -s . "${tmp_dir}"/*.json) \
  '{parentID: $parentID, query: $query, search: $search[0], traces: $traces[0]}' \
  > "${OUT_FILE}"

echo "wrote ${OUT_FILE}"
echo "traceIDs: ${trace_ids[*]}"
