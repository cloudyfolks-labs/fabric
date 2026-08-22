#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."

VERSION=$(cat VERSION)
BARE=${VERSION#v}

CHART=charts/fabric/Chart.yaml
CRD_CHART=charts/fabric/charts/fabric-crds/Chart.yaml
VALUES=charts/fabric/values.yaml

awk -v version="${VERSION}" '
  /^    kubeovn:$/ { pending = "      " }
  /^    repository: ghcr.io\/cloudyfolks-labs\/vpc-nat-gateway$/ { pending = "    " }
  /^    # -- DPDK image tag.$/ { pending = "    " }
  pending != "" && index($0, pending "tag: ") == 1 { sub(/tag: .*/, "tag: " version); pending = "" }
  { print }
' "${VALUES}" > "${VALUES}.tmp" && mv "${VALUES}.tmp" "${VALUES}"

awk -v version="${VERSION}" -v bare="${BARE}" '
  /^version: / { print "version: " bare; next }
  /^appVersion: / { print "appVersion: " version; next }
  /name: fabric-crds/ { print; dep = 1; next }
  dep && /^ *version: / { sub(/version: .*/, "version: " bare); dep = 0 }
  { print }
' "${CHART}" > "${CHART}.tmp" && mv "${CHART}.tmp" "${CHART}"

awk -v bare="${BARE}" '
  /^version: / { print "version: " bare; next }
  { print }
' "${CRD_CHART}" > "${CRD_CHART}.tmp" && mv "${CRD_CHART}.tmp" "${CRD_CHART}"

echo "chart version synced to ${VERSION}"
