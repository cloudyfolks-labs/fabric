#!/usr/bin/env bash
set -euo pipefail

GROUP="kubeovn.io"
FINALIZERS=("kubeovn.io/kube-ovn-controller" "kube-ovn-controller" "fabric.cloudyfolks.io/controller")
APPLY=false

usage() {
  cat <<'USAGE'
Usage: hack/strip-legacy-finalizers.sh [--apply]

Removes the fabric controller finalizers from every custom resource in the
kubeovn.io API group. fabric runs no controller for that group, so nothing
ever removes those finalizers and the CRD deletion step of the migration
blocks for ever.

The script is idempotent and reports every object it touches. Without
--apply it only reports what it would change.
USAGE
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --apply) APPLY=true; shift ;;
    -h|--help) usage; exit 0 ;;
    *) echo "unknown argument: $1" >&2; usage >&2; exit 2 ;;
  esac
done

if ! kubectl version >/dev/null 2>&1; then
  echo "kubectl cannot reach a cluster" >&2
  exit 1
fi

crds=$(kubectl get crd -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}' | grep -E "\.${GROUP}\$" || true)
if [ -z "${crds}" ]; then
  echo "no ${GROUP} CRDs are installed, nothing to do"
  exit 0
fi

crd_count=0
scanned=0
stale_count=0

while read -r crd; do
  [ -n "${crd}" ] || continue
  crd_count=$((crd_count + 1))
  scope_kind=$(kubectl get crd "${crd}" -o jsonpath='{.spec.scope}')
  jsonpath='{range .items[*]}{"-"}{" "}{.metadata.name}{" "}{.metadata.finalizers}{"\n"}{end}'
  if [ "${scope_kind}" = "Namespaced" ]; then
    jsonpath='{range .items[*]}{.metadata.namespace}{" "}{.metadata.name}{" "}{.metadata.finalizers}{"\n"}{end}'
  fi

  while read -r namespace name finalizers; do
    [ -n "${name}" ] || continue
    scanned=$((scanned + 1))

    stale=false
    for finalizer in "${FINALIZERS[@]}"; do
      case "${finalizers}" in
        *"\"${finalizer}\""*) stale=true ;;
      esac
    done
    ${stale} || continue
    stale_count=$((stale_count + 1))

    scope=()
    location="cluster"
    if [ "${scope_kind}" = "Namespaced" ]; then
      scope=(-n "${namespace}")
      location="namespace ${namespace}"
    fi

    if ${APPLY}; then
      kubectl patch "${crd}" "${name}" "${scope[@]}" --type=merge -p '{"metadata":{"finalizers":null}}' >/dev/null
      echo "patched ${crd} ${name} in ${location}"
    else
      echo "would patch ${crd} ${name} in ${location}: ${finalizers}"
    fi
  done < <(kubectl get "${crd}" --all-namespaces --ignore-not-found -o jsonpath="${jsonpath}" 2>/dev/null || true)
done <<< "${crds}"

echo "scanned ${scanned} object(s) across ${crd_count} ${GROUP} CRD(s), ${stale_count} carried a stale finalizer"
if ! ${APPLY} && [ "${stale_count}" -gt 0 ]; then
  echo "dry run: re-run with --apply to remove them"
fi
