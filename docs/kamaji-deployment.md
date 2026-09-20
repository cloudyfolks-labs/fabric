# Kamaji-style split-cluster deployment

In a Kamaji-style topology the tenant Kubernetes control plane
(apiserver, controller-manager, scheduler, etcd) runs as pods in a
**management cluster**. The tenant worker nodes run in a separate
**data-plane cluster**. Each tenant gets its own apiserver in the
management cluster.

fabric splits along the same boundary:

```
┌────────────────────────────────────────┐         ┌────────────────────────────────────────┐
│  Management cluster                    │         │  Tenant data-plane cluster             │
│                                        │         │                                        │
│  ┌────────────────┐                    │         │  ┌────────────────┐                    │
│  │ ovn-central    │  StatefulSet       │         │  │ fabric-        │                    │
│  │  + PVC         │  (1 or 3 replicas) │  TCP or │  │ controller     │                    │
│  └─────────┬──────┘                    │  SSL    │  └────────────────┘                    │
│            │                           │ <─────> │  ┌────────────────┐                    │
│  ┌─────────▼──────┐                    │ ovn-nb  │  │ ovs-ovn (DS)   │                    │
│  │ ovn-nb /       │  NodePort or       │ ovn-sb  │  │ fabric-cni     │                    │
│  │ ovn-sb /       │  LoadBalancer  ────┼─────────┼─►│ fabric-pinger  │                    │
│  │ ovn-northd svc │                    │         │  └────────────────┘                    │
│  └────────────────┘                    │         │  + Subnet / IP / Vpc CRDs in tenant   │
│                                        │         │    apiserver                           │
└────────────────────────────────────────┘         └────────────────────────────────────────┘
```

The same Helm chart is installed twice, with a different `installMode`
value and a different `--kube-context`. The `central.hcp` values block
turns `ovn-central` into a PVC-backed StatefulSet that the chart can
expose outside the cluster, and tells the data-plane components where
to find it.

## Component placement

| Component | Where it runs | Why |
|---|---|---|
| `ovn-central` (StatefulSet + PVC per replica, `ovn-nb` / `ovn-sb` / `ovn-northd` Services) | Management cluster, namespace `central.hcp.namespace` | Central OVN database. The PVC keeps the database when a pod moves. |
| `fabric-controller` | Tenant cluster | Watches the tenant Subnet, IP and Vpc CRs with in-cluster auth. |
| `ovs-ovn`, `fabric-cni`, `fabric-pinger` (DaemonSets) | Tenant cluster | They program the local OVS on every tenant node. |
| `fabric-monitor` (Deployment) | `installMode: full` only | It reads the local Unix sockets and database files of `ovn-central`. |
| fabric CRDs (`fabric.cloudyfolks.io/v1`) | Tenant apiserver | Tenants create Subnets against their own apiserver. |
| `fabric-tls` Secret | Both clusters, when `networking.enableSsl` is `true` | `ovn-central` serves SSL listeners; the tenant components use the client certificate. |

## Prerequisites

- Two reachable clusters with separate kubeconfigs or contexts
  (`mgmt`, `tenant`).
- A path from the tenant nodes to the `ovn-nb` and `ovn-sb` Services of
  the management cluster: a NodePort on a reachable node address, or a
  LoadBalancer.
- A StorageClass in the management cluster that supports detach from
  one node and attach on another node. See
  [single-replica-deployment.md](./single-replica-deployment.md).
- Recommended: set `networking.enableSsl: true` before you expose the
  OVN database ports outside the cluster. Plain TCP across a cluster
  boundary is not secure.

## Install

### 1. Management cluster: `controlPlaneOnly`

The chart does not create `central.hcp.namespace`. Create it first.

```yaml
# mgmt-values.yaml
namespace: kube-system
installMode: controlPlaneOnly

central:
  hcp:
    enabled: true
    namespace: hcp
    replicas: 1                          # 1 or 3; 3 makes a raft cluster
    nbAddress: tcp:10.99.99.99:30641     # the address the tenant nodes use
    sbAddress: tcp:10.99.99.99:30642
    service:
      type: NodePort                     # or LoadBalancer
      nbNodePort: 30641
      sbNodePort: 30642
    storage:
      storageClassName: my-csi           # empty selects the cluster default
      size: 5Gi

networking:
  enableSsl: true                        # recommended
```

```bash
kubectl --context=mgmt create namespace hcp
helm install --kube-context=mgmt fabric oci://ghcr.io/cloudyfolks-labs/charts/fabric \
  --version 1.2.1 -n kube-system -f mgmt-values.yaml
```

This release renders:

- the `ovn-central` StatefulSet with one `ovn-data` PVC per replica
- the `ovn-central` headless Service and the `ovn-nb`, `ovn-sb` and
  `ovn-northd` Services of type `central.hcp.service.type`
- the `ovn-central` ServiceAccount and its RBAC
- the `fabric-tls` Secret in `kube-system` and, when `central.hcp.namespace`
  differs, a copy in `central.hcp.namespace` when SSL is on
- the fabric CRDs, unless you set `crds.enabled: false`

It renders no agents and no controller.

With `service.type: LoadBalancer`, read the assigned address after the
install and put it into `nbAddress` and `sbAddress` of both releases.

The chart renders the `fabric-tls` Secret into `namespace` and, when
`central.hcp.namespace` differs, a copy with the same data into
`central.hcp.namespace` for `ovn-central`. `fabric-controller` renews
only the copy in `namespace`; a `helm upgrade` syncs the renewed data
into `central.hcp.namespace`. When SSL is on, also copy the Secret out
of the management cluster. The tenant cluster needs the same
certificate material.

```bash
kubectl --context=mgmt -n kube-system get secret fabric-tls -o yaml > fabric-tls.yaml
```

### 2. Tenant data-plane cluster: `dataPlaneOnly`

When SSL is on, seed the `fabric-tls` Secret into the tenant
`kube-system` namespace first. The chart refuses to render
`dataPlaneOnly` with `networking.enableSsl: true` when the Secret is
missing, because a locally generated certificate is not trusted by the
management cluster. In production, sync the Secret with a tool such as
`external-secrets` or `sealed-secrets` instead of `kubectl apply`.

```bash
kubectl --context=tenant -n kube-system apply -f fabric-tls.yaml
```

Then install the data-plane release. `nbAddress` and `sbAddress` are
passed verbatim to `ovn-controller` and `fabric-controller`. Use the
`ssl:` scheme when SSL is on.

```yaml
# tenant-values.yaml
namespace: kube-system
installMode: dataPlaneOnly

central:
  hcp:
    enabled: true
    nbAddress: ssl:10.99.99.99:30641
    sbAddress: ssl:10.99.99.99:30642

networking:
  enableSsl: true                        # must match the management cluster
```

```bash
helm install --kube-context=tenant fabric oci://ghcr.io/cloudyfolks-labs/charts/fabric \
  --version 1.2.1 -n kube-system -f tenant-values.yaml
```

This release renders:

- the fabric CRDs, installed into the tenant apiserver
- the `fabric-controller` Deployment with `OVN_NB_ADDR` and
  `OVN_SB_ADDR` set from `central.hcp`; it runs at most two replicas in
  this mode
- the `ovs-ovn` DaemonSet with `OVN_SB_ADDR` set the same way, and
  the `fabric-cni` and `fabric-pinger` DaemonSets
- the webhook, the FRR agent when enabled, and the related
  ServiceAccounts, ClusterRoles and bindings

It does not render `ovn-central` or its Services.

## Verify connectivity

On the tenant cluster:

```bash
kubectl --context=tenant -n kube-system rollout status deploy/fabric-controller

kubectl --context=tenant -n kube-system exec ds/ovs-ovn -- ss -tnp | grep ':30642'
# ESTAB ... <node IP>:<port> 10.99.99.99:30642 users:(("ovn-controller",...))

kubectl --context=tenant create -f - <<EOF
apiVersion: fabric.cloudyfolks.io/v1
kind: Subnet
metadata: {name: smoke}
spec:
  cidrBlock: 10.50.0.0/16
  default: false
EOF
```

A pod in a namespace bound to `smoke` gets an address from
`10.50.0.0/16`.

## Version lockstep

Both releases must run the same image tag (`global.images.fabric.tag`)
and the same chart version. The chart does not enforce this. When you
upgrade the management release, upgrade the tenant release in the same
window. OVN schema drift between `ovn-northd` and `ovn-controller` is
hard to diagnose.

A cross-cluster GitOps pattern keeps the versions aligned:

- Argo CD `ApplicationSet` with two Applications that share one values
  fragment and override only `installMode` and `central.hcp`.
- Flux `Kustomization` per cluster, each pinned to the same chart
  revision.

## Limitations

- The CRD subchart renders in both releases. Set `crds.enabled: false`
  on the management release if you do not want the CRDs there.
- `ovn-northd` (port 6643) does not need to be reachable across the
  cluster boundary. Only the management cluster components talk to it.
- `fabric-monitor`, the DPDK daemonset (`HYBRID_DPDK=true`) and the OVS
  upgrade hooks render under `installMode: full` only. They reference a
  local `ovn-central`.
- `nbAddress` and `sbAddress` take one address each. With a
  LoadBalancer, the `ovn-nb` and `ovn-sb` Services get separate
  addresses unless the provider supports a shared address. Put each
  assigned address into its own field.
