# FEP-0001: LoadBalancer Services

- Status: accepted
- Replaces: the lb-svc pod path (`--enable-lb-svc`)

## Summary

fabric implements Kubernetes `Service type=LoadBalancer` natively:
IPs come from declared pools, OVN balances on the VPC router, and the
VIP is announced by OVN ARP/GARP or by BGP through the FRR agent. No
per-service workloads. The `vpc-nat-gateway` image retires at the end
of the rollout.

## Goals

- A LoadBalancer Service gets an external IP from a pool, reachable
  from outside the cluster, balanced across endpoints, with
  `status.loadBalancer.ingress` set.
- Client source IP preserved by default.
- Works in the default VPC and in custom VPCs.
- Coexists with MetalLB or a cloud controller through
  `loadBalancerClass`.

## Non-goals

- `externalTrafficPolicy: Local` semantics before the OVN 26.03
  rebase.
- VIPs on networks that are not OVN provider networks.
- L7.

## API

### LoadBalancerPool (cluster-scoped)

```yaml
apiVersion: fabric.cloudyfolks.io/v1
kind: LoadBalancerPool
metadata:
  name: public-vlan
spec:
  subnet: ext-vlan100          # localnet Subnet; the IP source
  announce: l2 | bgp
  serviceSelector: {}          # optional labelSelector
  default: true                # at most one default pool
status:
  available: 27
  inUse: 5
```

The pool references a Subnet instead of defining CIDRs so fabric keeps
one IPAM: every allocation is an `OvnEip` against that Subnet.

### Service contract

| Field / annotation | Semantics |
|---|---|
| `spec.loadBalancerClass` | fabric claims `fabric.cloudyfolks.io/loadbalancer`, or unset when the controller runs with `--default-load-balancer-class` |
| `lb.fabric.cloudyfolks.io/address-pool` | pool name; unset selects the matching default pool |
| `lb.fabric.cloudyfolks.io/ips` | requested IP(s), comma-separated for dual-stack |
| `lb.fabric.cloudyfolks.io/allow-shared-ip` | sharing key; same key may share a VIP when ports do not overlap |
| `spec.sessionAffinity: ClientIP` | OVN `affinity_timeout` |
| `status.loadBalancer.ingress` | written after the OVN LB rows exist and announcement is active |

The Service's VPC is the namespace's VPC. The pool's Subnet must be in
the VPC router's external subnets; the controller reports a condition
when it is not and does not patch the Vpc itself.

## Datapath

1. Allocate an `OvnEip` (type `nat`) named `lb-<service-uid>` against
   the pool Subnet.
2. Attach the VPC's shared LBs to the VPC router (the RouterLBRule
   attach path).
3. Write `vip:port -> endpoints` rows, `ip_port_mappings`, and OVN
   health checks through the endpoint-slice path. Reject port
   conflicts with OvnDnatRule and other claimed Services.
4. ARP: the localnet LRP's default `neighbor_responder=reachable`
   answers on the gateway chassis. The controller sets
   `nat-addresses="router"` on the provider switch's router port so
   NAT and LB VIPs are announced by GARP on gateway failover.

## Announcement modes

- `announce: l2`: OVN ARP responder + GARP. Zero extra components.
  Requires L2 adjacency; failover is gateway-chassis failover.
- `announce: bgp`: OVN writes the VIP host route into the VPC VRF and
  the FRR agent redistributes it with the LRP as next hop. Gated by
  `redistribute: lb` on the VPC. ECMP-capable; no L2 requirement.

## Conformance behavior

| Contract point | v1 behavior |
|---|---|
| Client source IP | always preserved (DNAT-only router LB) |
| `externalTrafficPolicy: Local` | accepted, behaves as Cluster, condition + Event state it; real Local with OVN 26.03 `options:distributed` |
| Dual-stack | follows the pool Subnet protocol |
| Zero endpoints | `reject=true`: RST / ICMP unreachable |
| Health checks | OVN Service_Monitor; TCP/UDP; OVN-port backends only |

## Known limits

- Traffic centralizes on the VPC's gateway chassis until the OVN
  26.03 rebase.
- OVN LB group selection does not offload on ConnectX-class NICs.
- SCTP is balanced without health checks.

## Rollout

1. Ships behind `--enable-ovn-lb-svc`; the pod path is deprecated.
2. Next release: default on.
3. Following release: the pod path, `lb-svc.sh`, the
   `vpc-nat-gateway` image, and the `ovn-vpc-nat-config` ConfigMap
   are removed.

## Alternatives considered

- Keep the lb-svc pod: per-service privileged appliance; contradicts
  the product definition.
- Require MetalLB: mandatory second controller; cannot serve VIPs on
  OVN provider VLANs nodes are not attached to. Stays supported
  alongside fabric via `loadBalancerClass`.
- Switch-attached LB: OVN never answers ARP for a VIP on a bare
  logical switch.
- Pool CRD with own CIDRs: duplicates Subnet IPAM.
