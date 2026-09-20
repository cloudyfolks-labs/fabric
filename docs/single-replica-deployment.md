# Single-replica ovn-central deployment

`dist/images/install.sh` can run `ovn-central` as a single-replica
Deployment with the OVN database on a PersistentVolume, instead of the
default multi-replica raft cluster. On host failure the pod is
rescheduled to another node and reattaches to the PV, so the database
state is recovered.

The Helm chart has no single-replica mode. The chart option for a
PVC-backed `ovn-central` is `central.hcp`: a StatefulSet with one PVC
per replica, `central.hcp.replicas` set to 1 or 3. See
[kamaji-deployment.md](./kamaji-deployment.md).

## When to use it

| | Cluster (raft) mode | Single-replica mode |
|---|---|---|
| `ovn-central` replicas | one per master | exactly 1 |
| DB consistency | raft quorum | single ovsdb-server, no consensus |
| Storage | hostPath per master | one PVC, must be attachable on the new node after a failover |
| Failover time | seconds (leader election) | PV detach/attach time, typically minutes |
| Operational complexity | higher (quorum, raft repair) | lower |
| Suitable cluster size | medium / large | small to medium, or constrained environments |

Pick single-replica when you do not have three master nodes, or when
you prefer a simpler failure model and can accept a multi-minute
recovery window during host loss.

## StorageClass requirements

The PVC is `ReadWriteOnce`. For the pod to move between nodes, the
StorageClass must support **detach from the failed node and attach on
the new node**. Most network block and file CSIs do:

- NFS (`csi-driver-nfs`): easy to set up, well-tested for failover
- Cloud block storage (EBS, GCE PD, Azure Disk): works, but detach from
  a dead node can take several minutes
- Ceph RBD, Longhorn, Portworx: work, see the vendor docs for fencing
- `hostPath` provisioners (for example local-path-provisioner) **do
  not move**: the PV is bound to the original node and the new pod
  stays `Pending`

`volumeBindingMode: WaitForFirstConsumer` is recommended, so the PV
topology is chosen at the first pod schedule and not at PVC creation.

## Install

```bash
ENABLE_SINGLE_REPLICA_OVN=true \
  OVN_CENTRAL_STORAGE_CLASS=my-csi \
  OVN_CENTRAL_PVC_SIZE=10Gi \
  bash dist/images/install.sh
```

`install.sh` refuses to switch a running release between raft mode and
single-replica mode in place. Back up the database, delete the
`ovn-central` Deployment, and run the script again.

After the install, verify:

```bash
kubectl get pod  -n kube-system -l app=ovn-central          # 1 pod
kubectl get pvc  -n kube-system ovn-central-data
kubectl get svc  -n kube-system ovn-nb -o jsonpath='{.spec.selector}'
# Expected: {"app":"ovn-central"}, no ovn-nb-leader selector
```

## Failover drill

```bash
node=$(kubectl get pod -n kube-system -l app=ovn-central \
  -o jsonpath='{.items[0].spec.nodeName}')

kubectl cordon "$node"
kubectl delete pod -n kube-system -l app=ovn-central

kubectl wait pod -n kube-system -l app=ovn-central \
  --for=condition=Ready --timeout=5m
kubectl exec -n kube-system -l app=ovn-central -- ovn-nbctl show
```

For a real host-loss test (not `cordon` plus delete), the CSI driver
must support **fencing**. Without it the volume stays attached to the
dead node, and the new pod hangs in `ContainerCreating` until
Kubernetes force-detaches the volume, typically after 6 minutes or
more.

## Exposing the OVN DB to data planes outside the cluster

In a Kamaji-style topology `ovn-central` runs in a *management*
cluster, and `ovn-controller`, `fabric-cni` and `fabric-controller` run
in one or more *tenant* clusters. The tenant components cannot reach a
`ClusterIP` Service of the management cluster, so the `ovn-nb`,
`ovn-sb` and `ovn-northd` Services must be exposed with `LoadBalancer`
or `NodePort`.

Single-replica mode is a prerequisite for this with `install.sh`: with
one stable backend the LoadBalancer always has exactly one healthy
endpoint, and there is no leader flapping when the LB runs its own
health checks.

```bash
ENABLE_SINGLE_REPLICA_OVN=true \
  OVN_CENTRAL_STORAGE_CLASS=my-csi \
  OVN_CENTRAL_SERVICE_TYPE=LoadBalancer \
  OVN_CENTRAL_LB_IP=10.99.99.99 \
  OVN_CENTRAL_EXTERNAL_TRAFFIC_POLICY=Local \
  bash dist/images/install.sh
```

`OVN_CENTRAL_SERVICE_TYPE=LoadBalancer` requires `OVN_CENTRAL_LB_IP`,
so the three Services share one address. The script adds the MetalLB
annotation `metallb.universe.tf/allow-shared-ip: fabric-central` to the
Services. Other LoadBalancer providers ignore it.

The tenant data plane is a Helm release with `installMode:
dataPlaneOnly` and `central.hcp.nbAddress` / `central.hcp.sbAddress`
set to `tcp:10.99.99.99:6641` and `tcp:10.99.99.99:6642`. See
[kamaji-deployment.md](./kamaji-deployment.md).

> **Security note.** Do not expose the OVN DB over plain TCP outside
> the cluster. Enable SSL (`ENABLE_SSL=true` for `install.sh`,
> `networking.enableSsl: true` for the chart) and distribute the
> `fabric-tls` Secret to the tenant clusters before you point them at
> the LB.

## Backup and restore

Take a hot backup of the standalone DB at any time:

```bash
pod=$(kubectl get pod -n kube-system -l app=ovn-central -o name | head -n1)
kubectl exec -n kube-system "$pod" -- \
  ovsdb-client backup unix:/var/run/ovn/ovnnb_db.sock > ovnnb_backup.db
```

Restore with the bundled script:

```bash
bash dist/images/restore-ovn-nb-db.sh single ovnnb_backup.db
```

The script scales `ovn-central` to 0, runs a helper pod that mounts the
same PVC to copy the backup into place, then scales back to 1.

## Migrating between modes

**Cluster to single**: scale the existing cluster `ovn-central` to 0,
run `ovsdb-tool cluster-to-standalone` against one of the raft member
DB files to produce a standalone NB DB, reinstall in single mode
against an empty PVC, then load the standalone DB with
`restore-ovn-nb-db.sh single`.

**Single to cluster**: take a backup, reinstall in cluster mode on a
fresh hostPath, and load the backup into the leader with
`ovsdb-client`. Verify the NB DB content against the backup before you
delete the PVC.

## Limitations

- The `fabric-leader-checker` raft queries, `ovn_northd` lock stealing
  and the raft header backup are skipped in single-replica mode.
  Compaction and DB storage-status checks still run.
- `ovn-healthcheck.sh` uses `ovsdb-server/get-db-storage-status`
  instead of `cluster/status`, so the readiness probe does not detect
  raft-specific issues (there are none in this mode).
- During a failover, all `fabric-controller`, `ovs-ovn` and
  `ovn-controller` clients see Service connect errors until the new pod
  is `Ready`. They reconnect automatically. In-flight ovsdb
  transactions during the outage may be retried.
