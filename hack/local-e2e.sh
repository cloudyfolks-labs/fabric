#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."

SUITE=${1:-}

usage() {
  cat <<'USAGE'
Usage: hack/local-e2e.sh <suite>

Runs one e2e suite against a local kind cluster on the current docker
context. Suites:

  dynamic-routing   VPC dynamic routing (needs the vrf kernel module)
  metallb           MetalLB underlay + u2o
  rlr               Router LB rules
  lb-svc            The ovn loadbalancer service mode

On macOS use a colima VM (Docker Desktop's kernel has no vrf support):

  colima start --cpu 8 --memory 10 --disk 60
  colima ssh -- sudo apt-get install -y "linux-modules-extra-$(colima ssh -- uname -r)"
  colima ssh -- sudo modprobe vrf

The fabric image for the local architecture must exist
(make image-kube-ovn or make image-kube-ovn-arm64).
USAGE
}

if [ -z "${SUITE}" ]; then
  usage
  exit 2
fi

VERSION=$(cat VERSION)
if ! docker image inspect "kubeovn/kube-ovn:${VERSION}" >/dev/null 2>&1; then
  echo "kubeovn/kube-ovn:${VERSION} is not built; run make image-kube-ovn-arm64 (or image-kube-ovn)" >&2
  exit 1
fi

if ! docker run --rm --privileged alpine sh -c "apk add -q iproute2 && ip link add lvrfprobe type vrf table 9999" >/dev/null 2>&1; then
  case "${SUITE}" in
    dynamic-routing)
      echo "the docker engine kernel has no vrf support; see the colima notes in --help" >&2
      exit 1
      ;;
  esac
fi

export E2E_BRANCH=main E2E_IP_FAMILY=ipv4 E2E_NETWORK_MODE=overlay

# The framework's docker client reads DOCKER_HOST, not the cli context.
# Without this it talks to /var/run/docker.sock, which can be a stale
# Docker Desktop socket while the cluster lives in another engine.
if [ -z "${DOCKER_HOST:-}" ]; then
  DOCKER_HOST=$(docker context inspect --format '{{.Endpoints.docker.Host}}')
  export DOCKER_HOST
fi

# install.sh drops the kubectl-ko plugin into /usr/local/bin, which is
# root-owned on macOS. Give it a writable directory on PATH instead.
KUBECTL_KO_DIR="${HOME}/.local/share/fabric/bin"
mkdir -p "${KUBECTL_KO_DIR}"
export KUBECTL_KO_DIR
export PATH="${KUBECTL_KO_DIR}:${PATH}"

case "${SUITE}" in
  dynamic-routing)
    n_worker=2 make kind-init-ipv4
    UNTAINT_CONTROL_PLANE=false ENABLE_OVN_LB_SVC=true make kind-install-ipv4
    make vpc-dynamic-routing-e2e
    ;;
  metallb)
    n_worker=2 make kind-init-ipv4
    make kind-install-metallb-pool-from-underlay-ipv4
    make kube-ovn-underlay-metallb-e2e
    ;;
  rlr)
    n_worker=2 make kind-init-ipv4
    make kind-install-ipv4
    make kube-ovn-rlr-e2e
    ;;
  lb-svc)
    n_worker=2 make kind-init-ipv4
    ENABLE_LB_SVC=true CNI_CONFIG_PRIORITY=10 make kind-install-ipv4
    make kube-ovn-lb-svc-conformance-e2e
    ;;
  *)
    usage
    exit 2
    ;;
esac
