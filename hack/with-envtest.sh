#!/usr/bin/env bash
# Start a throwaway Kubernetes control plane (etcd + kube-apiserver, no
# kubelet, no containers) from the envtest binaries published by
# kubernetes-sigs/controller-tools, run a command against it and tear it
# down again. Inside the command KUBECONFIG points at the server and
# kubectl from the same release is first in PATH.
#
#   K8S_VERSION=1.34.1 hack/with-envtest.sh hack/server-dry-run.sh examples/after/*.yaml
#
# This needs no container runtime, which makes it usable where kind is not.
set -euo pipefail

K8S_VERSION=${K8S_VERSION:-1.34.1}
OS=$(uname -s | tr '[:upper:]' '[:lower:]')
ARCH=$(uname -m)
case "$ARCH" in
  x86_64) ARCH=amd64 ;;
  aarch64 | arm64) ARCH=arm64 ;;
esac
CACHE=${ENVTEST_CACHE:-${XDG_CACHE_HOME:-$HOME/.cache}/dra-migrate/envtest}
BIN="$CACHE/$K8S_VERSION-$OS-$ARCH"

if [ ! -x "$BIN/kube-apiserver" ]; then
  mkdir -p "$BIN"
  url="https://github.com/kubernetes-sigs/controller-tools/releases/download/envtest-v$K8S_VERSION/envtest-v$K8S_VERSION-$OS-$ARCH.tar.gz"
  echo "downloading $url" >&2
  curl -fsSL "$url" | tar -xz -C "$BIN" --strip-components=2
fi

WORK=$(mktemp -d)
pids=()
cleanup() {
  for pid in ${pids[@]+"${pids[@]}"}; do kill "$pid" 2>/dev/null || true; done
  # kube-apiserver shuts down gracefully, which can take a while. Nothing
  # here is worth waiting for.
  for _ in $(seq 1 30); do
    alive=0
    for pid in ${pids[@]+"${pids[@]}"}; do
      if kill -0 "$pid" 2>/dev/null; then alive=1; fi
    done
    [ "$alive" = 0 ] && break
    sleep 0.1
  done
  for pid in ${pids[@]+"${pids[@]}"}; do kill -9 "$pid" 2>/dev/null || true; done
  wait 2>/dev/null || true
  rm -rf "$WORK"
}
trap cleanup EXIT

free_port() {
  python3 -c 'import socket; s = socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1])'
}
ETCD_PORT=$(free_port)
PEER_PORT=$(free_port)
API_PORT=$(free_port)
TOKEN=$(head -c 16 /dev/urandom | od -An -tx1 | tr -d ' \n')

"$BIN/etcd" --data-dir "$WORK/etcd" \
  --listen-client-urls "http://127.0.0.1:$ETCD_PORT" --advertise-client-urls "http://127.0.0.1:$ETCD_PORT" \
  --listen-peer-urls "http://127.0.0.1:$PEER_PORT" >"$WORK/etcd.log" 2>&1 &
pids+=($!)

openssl genrsa -out "$WORK/sa.key" 2048 2>/dev/null
echo "$TOKEN,admin,admin,system:masters" >"$WORK/tokens.csv"

# Beta versions of resource.k8s.io are not served by default. Set
# RUNTIME_CONFIG=resource.k8s.io/v1beta1=true,resource.k8s.io/v1beta2=true
# to validate --api-version v1beta1/v1beta2 output on releases that still
# ship them.
runtime_config=()
if [ -n "${RUNTIME_CONFIG:-}" ]; then
  runtime_config=(--runtime-config "$RUNTIME_CONFIG")
fi

"$BIN/kube-apiserver" \
  --etcd-servers "http://127.0.0.1:$ETCD_PORT" \
  --cert-dir "$WORK/certs" --bind-address 127.0.0.1 --secure-port "$API_PORT" \
  --service-account-issuer https://kubernetes.default.svc.cluster.local \
  --service-account-key-file "$WORK/sa.key" --service-account-signing-key-file "$WORK/sa.key" \
  --token-auth-file "$WORK/tokens.csv" --authorization-mode RBAC \
  --service-cluster-ip-range 10.0.0.0/24 \
  --disable-admission-plugins ServiceAccount \
  ${runtime_config[@]+"${runtime_config[@]}"} >"$WORK/apiserver.log" 2>&1 &
pids+=($!)

export KUBECONFIG="$WORK/kubeconfig"
export PATH="$BIN:$PATH"
kubectl config set-cluster envtest --server "https://127.0.0.1:$API_PORT" --insecure-skip-tls-verify=true >/dev/null
kubectl config set-credentials admin --token "$TOKEN" >/dev/null
kubectl config set-context envtest --cluster envtest --user admin >/dev/null
kubectl config use-context envtest >/dev/null

ready=0
for _ in $(seq 1 60); do
  if kubectl get --raw /readyz >/dev/null 2>&1 && kubectl get namespace default >/dev/null 2>&1; then
    ready=1
    break
  fi
  sleep 1
done
if [ "$ready" != "1" ]; then
  echo "kube-apiserver did not become ready:" >&2
  tail -n 20 "$WORK/apiserver.log" >&2
  exit 1
fi
echo "kube-apiserver $(kubectl get --raw /version | python3 -c 'import json, sys; print(json.load(sys.stdin)["gitVersion"])') is ready" >&2

CREATE_NAMESPACES=1 "$@"
