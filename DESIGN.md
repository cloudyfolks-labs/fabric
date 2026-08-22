# Design principles

These rules govern every feature and API decision in fabric.

## Adopter first

fabric is a public project. A feature is never removed because one
platform does not use it. A subsystem is removed only when fabric ships
a strictly better replacement, or when it contradicts the product
definition: fabric is a routed fabric, where a public IP is a route and
NAT is logical flows.

## Standard APIs first

Where Kubernetes defines a contract, fabric implements the contract.
Where the ecosystem has a convention (MetalLB and Cilium pool and
annotation semantics, `loadBalancerClass` coexistence), fabric follows
the convention. fabric never encodes concepts of a specific downstream
platform in its APIs. Downstream platforms validate a design; they do
not shape it.

## Naming

- The API group for all CRDs is `fabric.cloudyfolks.io`.
- All annotations, labels, finalizers, and load balancer classes use
  `fabric.cloudyfolks.io` or one of its subdomains.
- Multi-network annotation keys compose as
  `<provider>.cloudyfolks.io/<key>`. The default provider is `fabric`,
  so primary-network keys read `fabric.cloudyfolks.io/<key>` and
  secondary-network keys read `<nad>.<namespace>.fabric.cloudyfolks.io/<key>`.
- Identifiers internal to the OVN and OVS databases (`external_ids`
  such as `kube-ovn.io/*` and `vendor=kube-ovn`) are wire contracts
  with the vendored OVN patches. They are not user API and do not
  change.
- All other names — binaries, container images, the Go module and
  package paths, Kubernetes object names, metric namespaces,
  environment variables, file paths, and CLI tools — carry no
  compatibility guarantee. fabric renames the names inherited from
  kube-ovn as the project converges on the fabric identity.

## Origin

fabric imported the kube-ovn source tree once and develops
independently. It does not track or sync upstream. The Apache-2.0
license and the upstream copyright headers apply to the imported code.
Upstream fixes or ideas can be ported deliberately, as normal changes
with review.

## Migration

fabric documents a migration path from kube-ovn for every breaking
divergence. The API group and annotation renames are breaking:
cluster-level CRs must be re-created under the new group, and workload
annotations must be rewritten. `MIGRATION.md` describes the procedure.
