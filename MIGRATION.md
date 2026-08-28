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
no resolver plugin. The controller resolves the domain names against
cluster DNS itself. Remove the kube-ovn CoreDNS resolver plugin and
its DNSNameResolver objects; fabric does not read them.

## Upgrading a running kube-ovn release

`helm upgrade` over an existing kube-ovn release fails on `ovs-ovn` and
`ovn-central`: fabric changes their immutable selector labels (F26 in
FINDINGS.md). Apply the fabric CRDs and the converted resources, then
delete the `kube-ovn-cni` and `kube-ovn-pinger` daemonsets, scale
`ovn-central` to zero, delete the `ovs-ovn` daemonset and the
`ovn-central` deployment, and run the upgrade. Between the deletion and
the new pods the datapath has no controller and no OVN databases, so
keep that gap as short as the upgrade command itself.

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

## Cluster-wide CNI outage

The window between step 1 and step 6 is a cluster-wide CNI outage. No
controller assigns IP addresses in that window. Each Pod that the
scheduler starts in the window stays in `ContainerCreating` until
fabric runs. Running Pods keep their network: ovn-central and ovs-ovn
continue to forward.

Do this before you start:

- Cordon all nodes with `kubectl cordon`. This stops new Pods on the
  nodes.
- Stop the cluster autoscaler, and stop all CI jobs and CronJobs that
  make Pods.
- Do not drain the nodes. A drain makes new Pods that cannot get an IP
  address.
- Keep the window short. Prepare and check the converted manifests
  before you stop the kube-ovn control plane.

Uncordon the nodes after step 6, when the fabric controller reports
that all Subnets are ready.

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
8. Remove the kube-ovn finalizers from the objects in the old API
   group. fabric runs no controller for that group, so nothing else
   removes them, and step 9 blocks for ever if they stay.

   ```
   hack/strip-legacy-finalizers.sh
   hack/strip-legacy-finalizers.sh --apply
   ```

   The first command reports what it changes. The second command makes
   the change. You can run both commands more than one time.
9. Delete the old `*.kubeovn.io` CRDs only after all CRs are converted
   and verified.

Pods with kube-ovn secondary NICs get new logical switch port names on
their next recreation because the provider suffix changed. Plan a
rolling restart for multi-NIC workloads.

## Rollback

Rollback is possible until step 9. After step 9 the old CRs are gone,
and you must restore them from the export you made in step 2.

1. Cordon all nodes again.
2. Stop the fabric control plane (controller, webhook). Do not stop
   ovn-central or ovs-ovn.
3. Remove the fabric finalizers from the fabric CRs. The script
   removes `fabric.cloudyfolks.io/controller` from the old group only,
   so use this command for the new group:

   ```
   for crd in $(kubectl get crd -o name | grep fabric.cloudyfolks.io); do
     kubectl get "${crd#customresourcedefinition.apiextensions.k8s.io/}" -A -o name |
       xargs -r -n1 kubectl patch --type=merge -p '{"metadata":{"finalizers":null}}'
   done
   ```

4. Delete the fabric workloads and the fabric CRDs.
5. Apply the CRs you exported in step 2, in the `kubeovn.io/v1` form.
6. Put back the `ovn.kubernetes.io/` annotations on Pods, Nodes,
   Namespaces and VirtualMachines. Set `spec.provider` back to
   `...ovn` in Subnet and NetworkAttachmentDefinition objects.
7. Install kube-ovn again. It adopts the same OVN database.
8. Uncordon the nodes.

The OVN northbound and southbound databases stay valid through the
rollback. Only the Kubernetes objects change.
