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
- Names inherited from kube-ovn that are not user API (binary names,
  Go package paths, image names) change only when there is a
  functional reason, to keep upstream syncs small.

## Upstream relationship

fabric tracks kube-ovn master as described in `UPSTREAM.md`. Features
are designed so that their mechanism could be proposed upstream; only
the naming differs. During a sync, upstream changes to renamed
identifiers resolve to the fabric names.

## Migration

fabric documents a migration path from kube-ovn for every breaking
divergence. The API group and annotation renames are breaking:
cluster-level CRs must be re-created under the new group, and workload
annotations must be rewritten. `MIGRATION.md` describes the procedure.
