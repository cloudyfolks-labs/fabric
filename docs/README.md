# Documentation index

The features inherited from Kube-OVN are documented in the upstream
[Kube-OVN documentation](https://kubeovn.github.io/docs/stable/en/).
Apply the API renames from [MIGRATION.md](../MIGRATION.md) when you
read those pages.

The fabric-specific documentation is in this directory:

- [dynamic-routing.md](dynamic-routing.md): how the `fabric-frr` agent
  advertises VPC routes over BGP
- [kamaji-deployment.md](kamaji-deployment.md): `ovn-central` in a
  management cluster, the data plane in tenant clusters
- [single-replica-deployment.md](single-replica-deployment.md):
  single-replica `ovn-central` on a PersistentVolume
- [proposals/](proposals/): feature proposals
- [release.md](release.md): the release process

Design rules are in [DESIGN.md](../DESIGN.md).
