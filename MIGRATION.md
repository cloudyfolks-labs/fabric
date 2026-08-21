# Migration from kube-ovn

fabric renamed the user-facing API domains. This is a breaking change.

| kube-ovn | fabric |
|---|---|
| CRD group `kubeovn.io/v1` | `fabric.cloudyfolks.io/v1` |
| Annotations/labels `ovn.kubernetes.io/<key>` | `fabric.cloudyfolks.io/<key>` |
| Multi-network keys `<provider>.kubernetes.io/<key>` | `<provider>.cloudyfolks.io/<key>` |
| Provider suffix `<nad>.<namespace>.ovn` | `<nad>.<namespace>.fabric` |
| Finalizer `kubeovn.io/kube-ovn-controller` | `fabric.cloudyfolks.io/controller` |
| Signer `kubeovn.io/signer` | `fabric.cloudyfolks.io/signer` |

Identifiers internal to the OVN and OVS databases do not change. The
OVN logical topology of an existing cluster stays valid.

`domainName` rules in AdminNetworkPolicy and ClusterNetworkPolicy need
the fabric build of the CoreDNS resolver plugin
(`ghcr.io/cloudyfolks-labs/fabric-dns`); the kube-ovn build watches the
old API group and does not work with fabric.

## New clusters

Install fabric directly. Use the fabric domains in all manifests.

## Existing kube-ovn clusters

A fresh install is the recommended path. An in-place migration is
possible with a maintenance window:

1. Stop the kube-ovn control plane (controller, webhook). Do not stop
   ovn-central or ovs-ovn; the datapath keeps forwarding.
2. Export all kube-ovn CRs. For each object: set `apiVersion` to
   `fabric.cloudyfolks.io/v1`, rename all `ovn.kubernetes.io/` keys in
   metadata, and remove `status`, `resourceVersion`, and `uid`.
3. Install the fabric CRDs and apply the converted CRs.
4. Rewrite annotations on live objects (Pods, Nodes, Namespaces,
   VirtualMachines): copy each `ovn.kubernetes.io/<key>` to
   `fabric.cloudyfolks.io/<key>`. Update `spec.provider` in converted
   Subnet and NetworkAttachmentDefinition objects from `...ovn` to
   `...fabric`.
5. Install fabric. The controller adopts the existing OVN database;
   logical switches, routers, and ports keep their names.
6. Update workload manifests (Deployments, VirtualMachine templates)
   to the new annotation keys before the next rollout.
7. Delete the old `*.kubeovn.io` CRDs only after all CRs are converted
   and verified. fabric removes the legacy
   `kubeovn.io/kube-ovn-controller` finalizer from converted objects
   automatically.

Pods with kube-ovn secondary NICs get new logical switch port names on
their next recreation because the provider suffix changed. Plan a
rolling restart for multi-NIC workloads.
