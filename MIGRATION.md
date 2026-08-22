# Migration from kube-ovn

fabric started as a fork of kube-ovn and develops independently. This
guide describes how to move a kube-ovn cluster or manifests to fabric.

## Renamed APIs

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

## Removed features

fabric does not carry these kube-ovn features. If your cluster uses
one, migrate to the replacement before you move to fabric.

| Removed | Replacement in fabric |
|---|---|
| BGP speaker (`--enable-bgp`, speaker pod) | Per-node FRR agent driven by the `BgpConf` CRD |
| `VpcNatGateway`, `IptablesEIP`, `IptablesFIPRule`, `IptablesDnatRule`, `IptablesSnatRule`, `QoSPolicy` | `OvnEip`, `OvnFip`, `OvnSnatRule`, `OvnDnatRule` |
| Pod annotations `eip` / `snat` | `OvnFip` / `OvnSnatRule` CRs |
| `ovn-external-gw-config` ConfigMap | `Vpc.spec.enableExternal` and `extraExternalSubnets` |
| `--enable-ovn-lb-prefer-local` | None; `externalTrafficPolicy: Local` support is planned |
| `--enable-external-vpc` (mirror foreign OVN routers as Vpc CRs) | None |
| STT tunnel encapsulation | Geneve (default) or VXLAN |
| `--mac-learning-fallback`, `--set-vxlan-tx-off` | Not needed |
| Kernel fastpath module | The OVN datapath does not need it |
| VpcEgressGateway BGP/EVPN announcer (`EvpnConf`) | The FRR agent; the egress gateway itself remains |
| `BgpConf.spec.advertiseLoadBalancerVips` | `Vpc.spec.dynamicRouting.redistribute: lb` |

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
3. Convert CRs of removed features to their replacements from the
   table above.
4. Install the fabric CRDs and apply the converted CRs.
5. Rewrite annotations on live objects (Pods, Nodes, Namespaces,
   VirtualMachines): copy each `ovn.kubernetes.io/<key>` to
   `fabric.cloudyfolks.io/<key>`. Update `spec.provider` in converted
   Subnet and NetworkAttachmentDefinition objects from `...ovn` to
   `...fabric`.
6. Install fabric. The controller adopts the existing OVN database;
   logical switches, routers, and ports keep their names.
7. Update workload manifests (Deployments, VirtualMachine templates)
   to the new annotation keys before the next rollout.
8. Delete the old `*.kubeovn.io` CRDs only after all CRs are converted
   and verified. fabric removes the legacy
   `kubeovn.io/kube-ovn-controller` finalizer from converted objects
   automatically.

Pods with kube-ovn secondary NICs get new logical switch port names on
their next recreation because the provider suffix changed. Plan a
rolling restart for multi-NIC workloads.
