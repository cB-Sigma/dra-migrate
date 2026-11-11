#!/usr/bin/env bash
# Validate manifests with a server-side dry run against the cluster of the
# current kubeconfig context. Nothing is persisted, except that namespaces
# named in the manifests are created when CREATE_NAMESPACES=1 (meant for
# throwaway clusters).
#
#   hack/server-dry-run.sh examples/after/*.yaml
set -euo pipefail

KUBECTL=${KUBECTL:-kubectl}

if [ "$#" -eq 0 ]; then
  echo "usage: $0 <file>..." >&2
  exit 2
fi

if [ "${CREATE_NAMESPACES:-0}" = "1" ]; then
  for ns in $(grep -h '^  namespace: ' "$@" | awk '{print $2}' | sort -u); do
    "$KUBECTL" get namespace "$ns" >/dev/null 2>&1 || "$KUBECTL" create namespace "$ns" >/dev/null
  done
fi

failed=0
for f in "$@"; do
  if out=$("$KUBECTL" apply --dry-run=server --validate=strict -f "$f" 2>&1); then
    printf 'ok    %s\n' "$f"
  else
    printf 'FAIL  %s\n' "$f"
    failed=1
  fi
  printf '%s\n' "$out" | sed 's/^/        /'
done
exit "$failed"
