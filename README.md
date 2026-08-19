# fabric

fabric is a Kubernetes network fabric for multi-tenant clouds. It gives
each tenant a real VPC with its own router, its own address space, and a
BGP path to the physical network.

fabric is a fork of [kube-ovn](https://github.com/kubeovn/kube-ovn), a
CNCF Sandbox Project. The OVN substrate, the CNI, and most of the
features come from kube-ovn. See [UPSTREAM.md](UPSTREAM.md) for the fork
base and how we sync upstream changes.

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

## What is different from kube-ovn

Added:

- **VPC dynamic routing**: the `BgpConf` CRD and the `kube-ovn-frr`
  per-node agent. VPC subnets, OVN EIPs, and BFD state advertise to the
  top-of-rack switches through FRR.

Removed, with the replacement in parentheses:

- BGP speaker (the FRR agent)
- VpcNatGateway, IptablesEIP/FIP/DnatRule/SnatRule, and QoSPolicy
  (OVN-native `OvnEip`/`OvnFip`/`OvnSnatRule`/`OvnDnatRule`)
- VpcEgressGateway BGP/EVPN announcer (the FRR agent; the egress
  gateway itself remains)
- Kernel fastpath module (the OVN datapath does not need it)

Everything else from kube-ovn remains: subnets, underlay/VLAN, security
groups, switch and router load balancers, KubeVirt live migration,
multi-cluster interconnect, and the rest.

## Documentation

The [kube-ovn documentation](https://kubeovn.github.io/docs/stable/en/)
applies to all inherited features. Documentation for the fabric-specific
features lives in this repository.

## Install

Helm charts and container images will publish under
`ghcr.io/cloudyfolks-labs` with the first release. Until then, build
from source:

```
make image-kube-ovn
make kind-init kind-install
```

## License

Apache-2.0. The imported kube-ovn code keeps its upstream copyright
headers. See [LICENSE](LICENSE).
