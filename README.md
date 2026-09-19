# fabric

fabric is a Kubernetes network fabric for multi-tenant clouds. It gives
each tenant a real VPC with its own router, its own address space, and a
BGP path to the physical network.

fabric started as a fork of
[Kube-OVN](https://github.com/kubeovn/kube-ovn) v1.17.0 and is now a
standalone project. The OVN substrate and the CNI come from that
import. fabric develops independently and does not track upstream.

## Direction

fabric builds on two layers:

1. **OVN below.** VPCs, routing, and NAT stay as OVN logical flows. The
   datapath is OpenFlow plus kernel conntrack. It has no iptables rules
   on the pod path, and it can offload to hardware.
2. **BGP at the edge.** A per-node FRR agent advertises VPC routes,
   external IPs, and gateway state to the top-of-rack switches. Dynamic
   routing replaces static plumbing between the cluster and the network.

On this base we plan an eBPF layer that reads the tenant identity OVN
already encodes on the wire: flow logs, per-EIP DDoS protection, and
identity-based security groups.

## What is different from Kube-OVN

Added:

- **VPC dynamic routing**: the `BgpConf` CRD and the `fabric-frr`
  per-node agent. VPC subnets, OVN EIPs, and BFD state advertise to the
  top-of-rack switches through FRR.
- **LoadBalancer Services**: the `LoadBalancerPool` CRD. VIPs come from
  a pool, OVN balances on the VPC router, and the VIP is announced by
  ARP or by BGP. Enabled with `--enable-ovn-lb-svc`. See
  [docs/proposals/](docs/proposals/).
- **Split-horizon DNS**: the `DnsZone` CRD. OVN answers the records in
  the datapath; no per-VPC DNS pods.

Removed, with the replacement in parentheses:

- BGP speaker (the FRR agent)
- VpcNatGateway, IptablesEIP/FIP/DnatRule/SnatRule, and QoSPolicy
  (OVN-native `OvnEip`/`OvnFip`/`OvnSnatRule`/`OvnDnatRule`)
- VpcEgressGateway and its BGP/EVPN announcer (OVN-native
  `OvnSnatRule` on the VPC router; the FRR agent announces the routes)
- Kernel fastpath module (the OVN datapath does not need it)

Everything else from Kube-OVN remains: subnets, underlay/VLAN, security
groups, switch and router load balancers, KubeVirt live migration,
multi-cluster interconnect, and the rest.

## Documentation

The features inherited from Kube-OVN are documented in the upstream
[Kube-OVN documentation](https://kubeovn.github.io/docs/stable/en/).
Read those pages with one systematic difference: fabric renamed the
API domains. Replace `kubeovn.io/v1` with `fabric.cloudyfolks.io/v1`,
and replace `ovn.kubernetes.io/` annotation keys with
`fabric.cloudyfolks.io/`. The full table is in
[MIGRATION.md](MIGRATION.md). The component names also differ:
`kube-ovn-controller`, `kube-ovn-cni`, `kube-ovn-pinger` and
`kube-ovn-monitor` are `fabric-controller`, `fabric-cni`,
`fabric-pinger` and `fabric-monitor` in fabric.

The fabric-specific documentation lives in this repository:

- [DESIGN.md](DESIGN.md): design rules
- [MIGRATION.md](MIGRATION.md): migration from Kube-OVN
- [docs/dynamic-routing.md](docs/dynamic-routing.md): the BGP path
- [docs/proposals/](docs/proposals/): feature proposals
- [docs/kamaji-deployment.md](docs/kamaji-deployment.md): split
  control-plane and data-plane installs
- [docs/single-replica-deployment.md](docs/single-replica-deployment.md):
  single-replica `ovn-central`
- [docs/release.md](docs/release.md): the release process

## Install

Each release publishes a Helm chart to
`oci://ghcr.io/cloudyfolks-labs/charts/fabric` and a multi-arch image
to `ghcr.io/cloudyfolks-labs/fabric`. The chart version equals the
release tag without the `v`:

```
helm install fabric oci://ghcr.io/cloudyfolks-labs/charts/fabric \
  --version 1.2.1 -n kube-system
```

The chart pulls `ghcr.io/cloudyfolks-labs/fabric:v1.2.1`. See the
chart [values](charts/fabric/values.yaml) for the options.

To build from source and test in kind:

```
make image-fabric
make kind-init kind-install
```

## License

Apache-2.0. The code imported from Kube-OVN keeps its upstream
copyright headers. See [LICENSE](LICENSE).

## Release

See [docs/release.md](docs/release.md). The GitHub Actions release
workflow is the only release path.
