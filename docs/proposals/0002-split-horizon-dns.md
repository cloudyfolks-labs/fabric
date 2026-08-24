# Split-horizon DNS as a service

- Status: implemented
- Replaces: the vpc-dns feature (per-VPC coredns Deployments)

## Problem

Workloads inside custom VPCs need name resolution for addresses that only
make sense inside their VPC, and different VPCs must be able to define the
same name with different answers. The previous vpc-dns feature solved this
by deploying a coredns Deployment per VPC, attached to both the VPC subnet
and the default subnet. That meant per-VPC pods to schedule, patch, and
monitor, an extra image to ship, and a template ConfigMap to maintain.

## Design

OVN carries a native DNS responder: rows in the northbound `DNS` table hold
a records map, logical switches reference them through `dns_records`, and
`ovn-controller` answers matching queries directly in the datapath on the
node where the query is made. No server receives the query and no packet
leaves the host.

fabric exposes this through a cluster-scoped CRD:

```yaml
apiVersion: fabric.cloudyfolks.io/v1
kind: DnsZone
metadata:
  name: acme-internal
spec:
  vpc: vpc-acme
  records:
    - name: db.internal.acme
      ips: ["10.0.0.5"]
    - name: api.internal.acme
      ips: ["10.0.1.7", "10.0.1.8"]
```

The controller reconciles each zone into one `DNS` row and attaches it to
every logical switch of the VPC, including subnets created after the zone.
Record names are lowercased and stored without a trailing dot. Queries that
match no record pass through unchanged to whatever upstream resolver the
workload is configured with.

Split horizon falls out of the datapath model: records are scoped to the
logical switches they are attached to, so two VPCs can define the same name
with different answers and each workload only ever sees its own view.

## Interactions

- A record can name any address: a RouterLBRule VIP, a workload address, or
  an external endpoint. Nothing about the zone depends on other features.
- Deleting a zone removes the `DNS` row; the weak references from logical
  switches clean up with it. A garbage collector removes rows whose zone no
  longer exists.

## Limitations

- Records are exact names with fixed answers. There is no forwarding,
  recursion, or wildcard matching; upstream resolution stays with the
  resolver the workload already uses.
- PTR records are not synthesized in the first version.
