# Dynamic routing

A VPC with `spec.dynamicRouting.enabled` advertises its EIPs to the
fabric over BGP. OVN writes the routes of the VPC into the Linux
routing table `vrfId` on the chassis that binds the external gateway
LRP. The `fabric-frr` agent on that chassis renders the FRR
configuration that redistributes the table into one BGP session set.

## What the agent renders

The agent renders one `router bgp <localAsn>` instance per node. Inside
its `address-family ipv4 unicast` it writes one line per VPC:

```
redistribute table-direct <vrfId> route-map KUBE-OVN-NH-<vpc>
```

`table-direct` reads the kernel table with id `vrfId` directly. FRR
never installs the routes of that table into the default table and
never creates a BGP instance per VRF. Adding or removing a VPC changes
one line and applies with `frr-reload.py`. The FRR container never
restarts for a configuration change, so the BGP sessions of the other
VPCs on the chassis stay up.

The route-map `KUBE-OVN-NH-<vpc>` sets the next hop of every route of
the VPC to the address of the external gateway LRP of that VPC. The
peer then forwards traffic for an EIP to the LRP of the owning VPC.

The outbound route-map `KUBE-OVN-OUT` permits the entries of
`spec.advertiseFilter` on the BgpConf, the CIDR of every subnet behind
a load balancer pool with `announce: bgp`, and the CIDR of every subnet
that a `nat` type OvnEip draws from. The pool and EIP subnets enter the
prefix-list as `ge 32 le 32`, so only host routes leave the chassis.

A VPC with dynamic routing has exactly one external subnet: the
default external subnet, or one entry in `spec.extraExternalSubnets`.
The controller rejects more entries. The agent takes the LRP of that
subnet as the BGP next hop and skips a VPC that carries more than one
gateway LRP.

`vrfId` must be at or below 65535. That is the highest table id
`table-direct` addresses. The controller rejects a higher value.

The agent does not import routes from the peer into the VPC tables.
Egress traffic of a VPC follows the static routes of the VPC through
the external subnet gateway. A VPC that needs a route to a fabric
prefix declares it in `spec.staticRoutes`.

## VRF devices and `maintainVrf`

The agent reads no VRF device. It renders the `table-direct` line for
every VPC that carries a dynamic routing spec, a `vrfId` and a ready
`lrp` OvnEip. A table that is empty or absent on a chassis advertises
nothing and produces no error. When the LRP moves to another chassis,
the table on the old chassis empties and the table on the new chassis
fills, and the advertisement follows without a change to the FRR
configuration.

`maintainVrf: true` lets ovn-controller create and delete the VRF
device that owns table `vrfId` on the gateway chassis. The option
remains supported and is the usual choice. It is not required for
advertisement: the agent never inspects `/sys/class/net`, and the
rendered configuration is the same with or without the device. With
`maintainVrf: false` the VRF device is the responsibility of the
operator, as described on the `vrfName` and `maintainVrf` fields of the
Vpc resource.
