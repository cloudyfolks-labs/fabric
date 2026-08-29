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

A VPC with dynamic routing and one external subnet, the default
external subnet or one entry in `spec.extraExternalSubnets`, advertises
through the LRP of that subnet. A VPC with more than one entry in
`spec.extraExternalSubnets` names the subnet whose LRP is the BGP next
hop in `spec.dynamicRouting.externalSubnet`. The controller rejects
more than one entry without that field, and rejects a field value that
is not one of the entries. The agent takes the LRP of the named subnet
and skips the VPC with a warning when that LRP has no ready OvnEip, or
when no subnet is named and the VPC carries more than one gateway LRP.

`vrfId` must be at or below 65535. That is the highest table id
`table-direct` addresses. The controller rejects a higher value.

The agent does not import routes from the peer into the VPC tables.
Egress traffic of a VPC follows the static routes of the VPC through
the external subnet gateway. A VPC that needs a route to a fabric
prefix declares it in `spec.staticRoutes`.

## Where each redistribute mode lands

The controller does not set `dynamic-routing-redistribute` on the
logical router. It sets the option on each LRP of the router. The
`nat` and `lb` modes go on the LRPs of the subnets of the VPC. The
`static` and `connected` modes go on every LRP, the external gateway
LRP included. A VPC that carries no subnet therefore advertises no
EIP and no VIP.

northd advertises through an LRP that carries the `nat` or `lb` mode
the NAT addresses and the load balancer VIPs of every router that
shares the peer logical switch with that LRP, so the external gateway
LRP of a VPC would carry the EIPs of every other VPC on the shared
external switch. FRR keeps one BGP route per prefix across all
`table-direct` tables, so a prefix that two tables hold leaves BGP as
soon as either table is flushed, and a gateway node that loses one VPC
would withdraw the EIPs of the other.

## VRF devices and `maintainVrf`

Leave `maintainVrf` at its default, `false`, on a VPC that the agent
advertises. ovn-controller then writes the routes of the VPC into the
plain kernel table `vrfId` and no VRF device exists. FRR follows every
route add and delete in such a table, so an EIP appears on the peer
when it is created and disappears when it is withdrawn or when the
gateway LRP moves to another chassis.

`maintainVrf: true` makes ovn-controller create a VRF device that owns
table `vrfId`. zebra then files the routes under that VRF, and
`table-direct` only copies them once, when the line is configured: a
route added or removed later never reaches BGP (`zebra/redistribute.c`,
`zebra_redistribute_is_table_direct`). The agent therefore skips a VPC
whose VRF device exists on the chassis and logs a warning that names
the device. Set `maintainVrf: true` only for an external FRR that runs
one BGP instance per VRF with `redistribute kernel`.

The agent renders the `table-direct` line for every VPC that carries a
dynamic routing spec, a `vrfId`, a ready `lrp` OvnEip and no VRF
device. A table that is empty on a chassis advertises nothing and
produces no error.
