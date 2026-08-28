# Findings

This file reports the state of the audit items F1 to F18 after the
remediation pass. Each section gives the outcome, the commits, the tests
and, where the work is not complete, what is left and why.

All unit tests in this report run with `go test ./pkg/...` on Linux.
No cluster was used: the end-to-end specs are new or changed code that
CI must run.

## F1 — Assigning a load balancer VIP destroys ClusterIP balancing

**Fixed.**

`getVipIps` is now the union of the SwitchLBRule vips, the RouterLBRule
vips, the Service cluster IPs and the external loadbalancer ingress IPs.
`handleUpdateEndpointSlice` uses the same function, so the OVN rows and
the garbage collector allow-list agree. Health checks stay limited to the
annotation vips, which keeps the behaviour of the synthetic headless
`rlr-` and `slr-` Services unchanged: they have no cluster IP, so the
union adds nothing for them.

- Commits: `2cd25c951`, `45b71e6c4`, `7d9ab544e`
- Tests: `Test_getVipIpsKeepsClusterIPsAlongsideRouterLbVip`,
  `Test_getVipIpsKeepsClusterIPsAlongsideSwitchLbVip`,
  `Test_serviceAnnotationVips` in `pkg/controller/service_test.go`.
  The two `Keeps` tests fail against the old `getVipIps`, which returned
  the annotation vips only.

The same change also repairs the MetalLB end-to-end suite. The ingress
IP of a `type: LoadBalancer` Service was programmed into the OVN load
balancer only when `--enable-ovn-lb-prefer-local` was set, and this fork
removed that flag. Without the row, an in-cluster client reached the VIP
through the node instead of the logical switch, so the backend saw the
node address and the reply of an `externalTrafficPolicy: Local` Service
left the VPC unmasqueraded. The union restores the row for every
`type: LoadBalancer` Service, which matches what kube-proxy does with a
loadbalancer ingress IP.

The MetalLB specs that asserted the backend runs on the announcing node
now assert only that the backend belongs to the Service. That locality
came from the removed prefer-local mode; the OVN load balancer selects
any backend.

The union skips the ingress address of a Service that carries the
`fabric.cloudyfolks.io/attachmentprovider` annotation. In that mode the
lb-svc gateway pod owns the address and does the DNAT itself, so an OVN
row for it would short-circuit the gateway pod. `LB Service E2E` covers
that path and is green today.

## F2 — The FRR agent cannot learn routes

**Fixed.**

`Render` now emits `no bgp ebgp-requires-policy` in the default instance
and `import vrf default` in every per-VPC instance.

The audit named `frr defaults traditional` as the first cause. The fix
does not change the profile: an explicit `no bgp ebgp-requires-policy`
holds whichever way the FRR release resolves the profile default, and it
matches the hand-written chassis configuration the e2e suite uses. The
profile also sets the BGP timers, so changing it would alter the session
timers of every existing deployment for no gain.

- Commits: `1d342790d`
- Tests: `TestRenderLearnsRoutesFromPeer` and the `TestRenderFull`
  expectations in `pkg/frr/render_test.go`
- End-to-end: the agent-managed spec now asserts the learn direction —
  the fabric loopback appears in the VRF kernel table and the workload
  pod reaches it (`ad0fe396c`).

## F3 — Migration deadlocks on dangling finalizers

**Fixed.**

`hack/strip-legacy-finalizers.sh` walks every CRD in the `kubeovn.io`
group, which covers the five removed types and every surviving type, and
removes `kubeovn.io/kube-ovn-controller`, `fabric-controller` and
`fabric.cloudyfolks.io/controller` from their objects. It discovers the
CRDs from the cluster, so it cannot miss a type. It defaults to a dry
run, reports every object it touches and can run more than one time.

`MIGRATION.md` runs it in step 8, before the CRD deletion in step 9, and
has two new sections: a rollback procedure and a cluster-wide CNI outage
warning that tells the operator to cordon the nodes and not to drain
them.

- Commits: `fd3276584`, `7477bc11c`
- Tests: none. The script needs a cluster, and this pass ran none.
  `bash -n` passes.

## F4 — Gateway chassis is never re-elected for the external gateway LRP

**Fixed.**

- `OVNNbClient.UpdateGatewayChassises` converges: it rewrites the
  priorities, drops the members that are no longer wanted and adds the
  missing ones. It replaces `CreateGatewayChassises`.
- `OVNNbClient.DeleteGatewayChassisByChassisName` removes the rows that
  outlive their chassis. `deleteNode` and `gcChassis` call it.
- `reconcileVpcExternalGatewayChassis` runs on every VPC reconcile from
  `handleUpdateVpcExternal`, for the default external subnet and for
  every extra external subnet.
- `enqueueVpcExternalGatewayByNodeChange` enqueues the external VPCs
  when a node carries the `external-gw` label before or after the
  change, so label removal and node deletion are both caught.
- The per-VPC deterministic shuffle moved into
  `externalGatewayChassises` unchanged.

Three guards protect a running VPC:

- A node whose CNI has not published its chassis yet is skipped instead
  of failing the reconcile.
- The chassis set is cleared only when no node carries the label at all,
  not when every labelled node lost its chassis annotation.
- A VPC with `enableBfd` converges the members only and keeps the
  priorities, because the BFD status handler raises and lowers them on
  the same LRP. Rewriting them would undo a BFD failover.

- Commits: `d664a6d7a`, `6c61bf509`, `bf5a779fb`, `a8f8ee128`,
  `b8b95cf64`
- Tests: `Test_UpdateGatewayChassises`,
  `Test_UpdateGatewayChassisMembers`,
  `Test_DeleteGatewayChassisByChassisName`,
  `TestGatewayChassisPriorities` in `pkg/ovs`;
  `TestEnqueueVpcExternalGatewayByNodeChange`,
  `TestEnqueueDeleteNodeEnqueuesExternalVpc`,
  `TestVpcExternalSubnets` in `pkg/controller`
- End-to-end: the failover step of the dynamic routing suite now removes
  the `external-gw` label and waits until the LRP drops that chassis
  (`ad0fe396c`).

The suite used `ovn-nbctl lrp-del-gateway-chassis` to move one VPC. That
is no longer a stable operation, because the controller owns the chassis
set and puts the row back on the next reconcile. Both failover specs now
use the label, and the multi-VPC spec computes which VPCs sit on the
node it takes down and asserts the others keep their original path.

## F5 — A BGP load balancer pool still needs an attached LRP

**Fixed.**

`checkPoolAnnouncePath` splits the two announce modes. `announce: l2`
still needs a ready LRP `OvnEip` on the pool subnet. `announce: bgp`
needs no LRP on the pool subnet: the pool subnet can be a provider-less
IPAM pool. Instead it needs the VPC to advertise loadbalancer routes,
that is `dynamicRouting.enabled` with `lb` in `redistribute`.

- Commits: `6cb6e5066`
- Tests: `TestVpcAdvertisesLoadBalancerVips`, `TestCheckPoolAnnouncePath`
- End-to-end: `45de60160`, see F10.

Deviation from the wording of the item: the misconfiguration condition
lands on the Service, with reason `DynamicRoutingNotReady`, and on an
Event, not on the `LoadBalancerPool`. One pool serves Services in many
VPCs, so "the VPC does not advertise lb routes" is a property of the
pair, not of the pool. F14 makes the pool condition report pool state
only, with one writer.

## F6 — Freed VIPs can be reassigned while stale backend rows survive

**Fixed.**

`cleanupRouterLBVips` with a nil scope now lists every load balancer that
holds one of the vips and deletes the rows from all of them. The
documented contract and the log lines are true again.

- Commits: `6151a97f4`, `c27bf4bb2`
- Tests: `Test_cleanupRouterLBVipsUnscoped`, which fails against the old
  code with "missing call(s) to LoadBalancerDeleteVip";
  `Test_handleDelRouterLBRule/VPC not found still deletes the VIP from
  every LB that holds it`

## F7 — Load balancer EIPs leak permanently

**Fixed with a garbage collector sweep.**

`gcOvnLbSvcEips` runs in the GC loop. It lists the `OvnEip` objects that
carry `LoadBalancerServiceLabel`, and deletes the ones that no live
claimed Service references, either through the vip annotation or as the
owner of the `lb-<uid>` name.

A Service finalizer was rejected: a finalizer on a user owned Service
turns a stopped controller into a cluster-wide block on Service
deletion, which is worse than the leak it prevents. The sweep touches
only fabric owned objects and heals the state the controller missed.

- Commits: `7a524ba3b`
- Tests: `TestGcOvnLbSvcEipsReleasesOrphans`

## F8 — `vrfId` is effectively mandatory but is neither required nor allocated

**Fixed.**

- `ValidateVpc` rejects an unset `vrfId` when dynamic routing is enabled.
- It rejects 253, 254 and 255, the reserved host routing tables.
  The audit asked for a range constraint; a plain maximum would be wrong
  because a Linux table id is 32 bits, so the CRD carries a CEL rule
  `self < 253 || self > 255` next to `minimum: 1` and
  `maximum: 4294967295`.
- `ValidateVpcVrfID` rejects an id another dynamic routing VPC already
  uses. Both the create and the update webhook call it.
- The API documentation no longer claims a default; it says the field is
  required and unique.

- Commits: `a4cfc5caa`
- Tests: `TestValidateVpc` cases "dynamic routing without vrf id" and
  "dynamic routing with reserved vrf id", `TestValidateVpcVrfID`

## F9 — `dynamicRouting.enabled` with `enableExternal: false` is a silent no-op

**Fixed.**

`ValidateVpc` rejects the combination, and `VpcSpec` carries a CEL rule
so the API server rejects it even when the webhook is off.

- Commits: `dddfe5859`
- Tests: `TestValidateVpc` case "dynamic routing without external
  gateway"

## F10 — `advertiseLoadBalancerVips` advertises with the wrong next hop

**Fixed by removing the broken mechanism.**

`BgpConf.spec.advertiseLoadBalancerVips` is gone, together with the
origin `network <ip>/32` lines, `no bgp network import-check` and the
`Networks` field of `VpcAdvertisement`. `redistribute: lb` survives: OVN
writes the vip route into the VPC VRF and the per-VPC route-map gives it
the LRP next hop. `MIGRATION.md` and the loadbalancer proposal record
the replacement.

- Commits: `37cb08eca`
- Tests: `TestRenderAdvertisesWithLrpNextHop`, which asserts there is no
  origin network, that the kernel routes carry the next-hop route-map,
  and that the route-map sets the LRP address
- End-to-end: a new spec allocates a vip from a provider-less pool with
  `announce: bgp`, checks the ToR learns `<vip>/32` with the LRP as next
  hop, reaches the backend through it and checks the withdrawal
  (`45de60160`). The dynamic routing CI job now installs with
  `ENABLE_OVN_LB_SVC=true`; the spec skips when the controller does not
  carry `--enable-ovn-lb-svc=true`.

## F11 — Adding a VRF instance restarts FRR

**Fixed.**

`f92bdde06` removed the restart branch. `28c7dcce4` and `cab92203a`
put it back: `frr-reload.py` reported success while leaving a new VRF
instance without working kernel redistribution, so the reload loop
exited whenever the sorted set of `router bgp ... vrf` lines changed.
The loop is PID 1 of the FRR container, so every VPC that gained or
lost a VRF on a chassis restarted FRR there and dropped every BGP
session on that chassis.

The agent now renders one default `router bgp <asn>` instance with
`redistribute table-direct <vrfId> route-map KUBE-OVN-NH-<vpc>` per
VPC. There is no per-VRF instance and no `import vrf`. Adding or
removing a VPC changes one line inside the address family and applies
with `frr-reload.py`; the restart branch of the reload loop is gone.
`ValidateVpc` rejects `vrfId` above 65535, the `table-direct` range.
The agent no longer imports peer routes into the VPC tables (F25); a
VPC reaches a fabric prefix through its static routes. The design is
recorded in `docs/dynamic-routing.md`.

- Commits: `1803192b0`
- Tests: `TestRenderFull` pins the whole rendered configuration for
  two VPCs; `TestRenderSingleBgpInstance` asserts one `router bgp`
  line and no ` vrf `, `import vrf` or `redistribute kernel`;
  `TestReloadScriptAppliesEveryChangeByReload` asserts the loop has no
  `exit 0` or restart path; `TestValidateRenderInput` rejects table id
  0 and 65536; `TestValidateVpc` rejects `vrfId` 65536 and accepts
  65535
- End-to-end: the agent spec keeps the ToR and failover assertions.
  The learned-route assertion had no equivalent under `table-direct`
  and was replaced by a static route for the fabric loopback through
  the ToR; the egress ping now proves the reply follows the advertised
  EIP route. The path against FRR 10.7 is left to the dynamic routing
  CI job of the pull request; it was not run locally.

## F12 — BGP ASN cannot express the 4-byte private range

**Fixed.**

`localASN`, `peerASN` and `peers[].asn` carry
`+kubebuilder:validation:Format=int64` with a 0 to 4294967295 range, so
the rendered schema is `format: int64` and the private range
4200000000-4294967294 is accepted.

- Commits: `21a406e75`
- Tests: none beyond the generated schema. The change is a CRD schema
  constraint; the Go type was already `uint32`.

## F13 — No status anywhere in the dynamic routing path

**Partly fixed.**

`BgpConf` has a status subresource. Every FRR agent writes its own entry
into `status.nodes`: the node name, the serial of the configuration it
rendered, the state (`Applied`, `Pending` or `Failed`), the failure
detail and a timestamp. The write uses a conflict retry and skips when
nothing changed. This is the last-applied result the item asks for.

- Commits: `e493f608e`, `6a176ede5` for the agent RBAC on
  `bgp-confs/status`
- Tests: `TestNodeApplyState`

**Not done: peer session state.** The design, and why it is not in this
pass:

The agent cannot read the session state today. `fabric-frr` and `frr`
are separate containers of the same pod. They share `/etc/frr` only, so
the agent has no `vtysh` binary and no `/var/run/frr` socket. Reading
`show bgp summary json` needs three changes that are larger than a
status pass: mount `/var/run/frr` into the agent container, add `vtysh`
to the fabric image, and add a poll loop with its own failure handling.

The design to finish it:

1. Mount the FRR run directory into the agent container and add `vtysh`
   to the image.
2. Poll `vtysh -c "show bgp summary json"` on the reassert ticker.
3. Extend `BgpNodeStatus` with `peers []{address, remoteAs, state,
   uptimeSeconds, prefixesReceived}`.
4. Extend `VpcStatus` with `dynamicRouting {vrfPresent, advertised,
   learned}` fed from the southbound `Advertised_Route` and
   `Learned_Route` tables, which the controller already has models for
   in `pkg/ovsdb` but never reads.

Step 4 needs a southbound reader in the controller and is the larger
half of the work.

## F14 — Load balancer pool `Ready` is write-only-False

**Fixed.**

- The periodic pool resync is the only writer of the pool condition. It
  computes the state from pool level facts alone: subnet missing gives
  `ExternalSubnetNotReady`, no available address gives `PoolExhausted`,
  otherwise `PoolReady` with `ConditionTrue`. Service reconciles no
  longer write the pool condition, which removes the write storm at its
  root.
- The write reads the live pool and retries on conflict, so a status
  update cannot be lost.
- A new webhook rejects a `LoadBalancerPool` whose `spec.subnet` does
  not exist, and rejects a second pool with `default: true`.
- A deleted pool already requeued every claimed Service. The Service
  reconcile now releases the vip and the EIP when it can no longer
  select a pool, instead of dropping the item.

- Commits: `d09c3b53f`, and `f3a51dacf` for a chart defect the same
  webhook work uncovered: the rendered
  `ValidatingWebhookConfiguration` nested `objectSelector` under the
  resource list, which the API server rejects. Chart CI had never run,
  so nothing caught it.
- Tests: `TestLoadBalancerPoolReadiness`,
  `TestUpdateLoadBalancerPoolUsageSetsReady`

Remaining: the `LoadBalancerPool` has no finalizer. It does not need
one now, because pool deletion requeues the Services and each Service
releases its own vip, and the garbage collector sweep from F7 catches
whatever the delete event missed. A finalizer would only make the
release ordered, not more complete.

## F15 — IPv6 in the FRR path is unimplemented

**Fixed by an explicit refusal.**

`ValidateRenderInput` rejects an IPv6 neighbour address, an IPv6 LRP
address and an IPv6 advertise filter entry, each with a message that
says dynamic routing supports IPv4 only. The prefix length bound of a
filter entry dropped from 128 to 32. The `BgpConf` API documentation
states the limit, so it appears in the CRD description.

- Commits: `27e578853`
- Tests: the `neighbor is ipv6`, `lrp address is ipv6` and
  `filter is ipv6` cases of `TestValidateRenderInput`

## F16 — Rename leftovers

**Fixed.**

- The TLS cert hash annotation is `fabric.cloudyfolks.io/tls-cert-hash`.
  The transition is handled: the reader accepts the old
  `kube-ovn.io/kube-ovn-tls-cert-hash` key when the new one is absent,
  and every write drops the old key. A live Secret moves over without a
  certificate rotation.
- `docs/kamaji-deployment.md` uses `fabric.cloudyfolks.io/v1` in the
  table and in the copy-and-paste Subnet manifest.
- The observability e2e annotation is
  `e2e.fabric.cloudyfolks.io/observability-config-version`.

- Commits: `6c993948f`, `cc5924618`, `16a5fc7a1`
- Tests: `TestKubeOVNTLSCertHashReadsLegacyAnnotation`

## F17 — Release plumbing is inconsistent

**Fixed by deleting `hack/release.sh`.**

The script queried the fabric repository, pushed to Docker Hub,
referenced a sibling docs repository that does not exist and branched on
`master`. `.github/workflows/release.yaml` is now the only release path,
and `docs/release.md` describes it.

`hack/sync-version.sh` copies `VERSION` into the chart image tags, the
chart version, the chart appVersion and the CRD subchart version.
`make sync-version` runs it and `make verify-version` fails when they
drift; the lint job runs `make verify-version`. The chart now says
`v1.17.0`, which matches `VERSION`.

- Commits: `e29054e7d`, `ee31423ce`
- Tests: `make verify-version` in CI

The `natGw` image tag still said `v0.1.0` and now tracks `VERSION` too.
`charts/fabric/README.md` is generated by helm-docs and was regenerated;
CI does not check it, and `docs/release.md` says to regenerate it.

Not changed: `dist/images/install.sh` still defaults `REGISTRY` to
`docker.io/kubeovn`. The kind targets load a locally built image with
that name, so changing it would break every e2e job. It is a separate
piece of work that has to move the image name in the Makefile and in
the workflows at the same time.

## F18 — Chart CI has never run

**Fixed.**

`Lint and Test Charts` now triggers on push to `main` as well as on a
pull request. The workflow has two jobs: `lint` runs `ct lint --all` on
every trigger and needs no cluster; `install` keeps the kind cluster
step and runs on a pull request only, because it installs the chart with
its default image reference, which needs a published release.

- Commits: `d2100683c`
- Tests: `helm lint ./charts/fabric` passes, and
  `helm template` renders every template.

## The failing CI jobs

The last run on `main` before this pass, `32540471186`, failed in two
places.

**`OVN METALLB E2E` (ipv4, ipv6, dual), all nine specs.** Cause and fix
in F1: the loadbalancer ingress IP stopped being programmed into the OVN
load balancer when `--enable-ovn-lb-prefer-local` was removed. Three
specs also asserted backend locality that the removed mode provided;
they now assert Service membership.

**`Kube-OVN Conformance E2E (ipv4, overlay)`,
`test/e2e/kube-ovn/router_lb_rule/router_lb_rule.go:310`, "EIP must have
at least one IP address".** A race in `patchOvnEipStatus`. It read the
`OvnEip` from the lister, which can still hold the empty spec that
`createOrUpdateOvnEipCR` has just updated, and wrote an empty `v4Ip` and
`macAddress` over the allocated ones while marking the EIP ready. The
status fields have no `omitempty`, so the merge patch cleared them. The
function now reads the live object. Commit `7e507e3a0`.

Neither fix was run against a cluster in this pass.

## Post-audit addendum: dynamic routing was dead at the chassis

Running the suite locally (hack/local-e2e.sh, added after the audit)
surfaced what CI silence had hidden: ovs-ovn runs ovn-controller as
nobody with added pod capabilities, which a non-root process never
receives without file capabilities, so every route_exchange VRF create
failed with EPERM. Sessions established and routes were learned, but
nothing was ever advertised. The image now grants CAP_NET_ADMIN to
ovn-controller the same way the other fabric binaries get their file
capabilities, and the failover spec passes end to end: advertise,
learn, egress via the learned route, label-driven re-election, ToR
relearn from the standby, withdrawal.

The CI job itself had never run a single spec (the vrf kernel module
is absent on the hosted runner), which is why none of this surfaced in
any green run. The job installs the module now and the suite fails on
an empty focus.

## Local verification

- `go build ./...` and `go vet ./pkg/... ./cmd/... ./test/...` pass for
  `GOOS=linux`.
- `golangci-lint run` passes with the CI version, v2.12.2.
- `go test ./pkg/...` passes except `TestDetectIPConflict` in
  `pkg/util`. That test sends a real ARP probe on the host network and
  finds a neighbour inside a container on this workstation. It passed in
  the last CI run of the `Lint and unit test` job.
- `go mod tidy`, `make gen-crd` and `make verify-version` leave no diff.
- `helm lint` and `helm template` pass for the chart.

## F19: Withdrawal of a VPC can remove all VRF devices on its gateway chassis

**Fixed on the agent side.** The OVN sweep below is still as observed.

Observed once on the local e2e rig during the multi-VPC isolation spec.
Three VPCs held dynamic-routing VRFs. Two VPCs failed over to the
second gateway node, which then held all three VRFs. The test then
withdrew the EIP and FIP of the VPC that was native to that node. After
the withdrawal, the node had zero VRF devices. The FRR agent still
rendered all BGP VRF instances, but with no kernel VRFs there was
nothing to redistribute, and the two surviving VPCs lost their
advertisements permanently.

The VRF devices belong to ovn-controller route_exchange, so the defect
is in the OVN layer teardown path, not in the FRR agent. The trigger is
sensitive to which chassis hosts the withdrawn VPC. The solo run of the
same spec, where the withdrawn VPC was one of the moved VPCs, passes.

Status: the agent no longer depends on the VRF device. It gates on
the dynamic routing spec of the VPC and renders `table-direct` for the
table id, so a swept VRF only empties the table until ovn-controller
recreates it, and the surviving VPCs keep their configuration lines
and recover without an agent change. The `/sys/class/net` poll and
the VRF signature are gone. `maintainVrf` remains supported and is
not required for advertisement (`docs/dynamic-routing.md`). The e2e
spec still always withdraws a moved VPC. The OVN route_exchange sweep
is still worth tracking, but it no longer takes advertisement down.

- Commits: `f7586ee7c`
- Tests: `TestVpcAdvertisementsNeedNoVrfDevice` asserts a VPC with a
  dynamic routing spec, a `vrfId` and a ready LRP is rendered on a
  host without any VRF device, and that VPCs without the spec, without
  a `vrfId` or without an LRP are not

Analysis (2026-08-28), against OVN branch-25.03 as built into the
`kubeovn/kube-ovn-base:v1.17.0` image (the image clones the branch at
build time; the current image was built on 2026-08-27):

- The 25.03 branch already carries every route-exchange fix that
  landed on it: `cec627b` (netlink failures on VRF and route updates),
  `14f9a15` (stop deleting non-existent VRFs), `e4ab209` (retry on
  error). None of them changes the VRF sweep described below.
- Upstream `main` adds `8874d9d` and `ce663bb` (June 2026). They let
  several datapaths share one VRF table and move the per-table sync
  after the datapath loop. They do not touch the sweep, and they do
  not apply to 25.03 without a manual port. They are not the fix.
- The only code path that removes every VRF at once is the end of
  `route_exchange_run` in `controller/route-exchange.c`. Each run
  swaps `_maintained_vrfs` into an old set, re-adds one name per
  datapath in `announce_routes`, and deletes every name that was not
  re-added. So one run in which `announce_routes` holds no entry for
  the surviving routers deletes their VRFs, and nothing recreates a
  VRF until a later run lists the router again. Two skips inside the
  loop happen before the re-add: an invalid or duplicate table id,
  and a failed `re_nl_create_vrf`. A duplicate table id therefore
  costs the datapath its VRF. The isolation spec uses distinct ids
  (1101..1103), so the duplicate skip is not the trigger there.
- `route_exchange_cleanup_vrfs` runs on every graceful exit of
  `ovn-controller` (`controller/ovn-controller.c`). A restart of the
  ovs-ovn pod on a gateway node therefore removes all VRF devices on
  that node and recreates them on the first run after restart. This
  is by design upstream, but it means a controller restart during a
  withdrawal is a candidate for the observed state.
- fabric's option placement is correct: `dynamic-routing-vrf-name`
  and `dynamic-routing-vrf-id` on the logical router, and
  `dynamic-routing-maintain-vrf` on the gateway LRP. northd copies
  the first two from the router and the third from the port
  (`northd/northd.c`).

Next steps, in order:

1. Reproduce on a rig with `ovn-appctl -t ovn-controller vlog/set
   route_exchange:dbg exchange:dbg route_exchange_netlink:dbg` on the
   gateway nodes, and record `ip -d link show type vrf` after each
   step of the isolation spec. The log line "Unable to sync routes
   for datapath" or an ovn-controller restart in the same window
   pins the branch above.
2. If the sweep is the cause, the durable fix is on the fabric side:
   let the FRR agent own the VRF devices (`maintainVrf: false` on the
   VPC, create and delete the devices from the VPC list the agent
   already renders). That removes the OVN sweep from the failure
   domain and survives ovn-controller restarts without a VRF flap.

## F20 — EIP pool CIDRs never reach the advertise filter

**Fixed.** The outbound prefix-list now carries the CIDR of every
subnet that a `nat` type OvnEip draws from, as `ge 32 le 32`, next to
the `announce: bgp` pool CIDRs.

- Commits: `fa46bf96e`
- Tests: `TestAdvertisedSubnetNamesIncludeNatEipSubnets` asserts the
  bgp pool subnet and each nat EIP subnet appear once and the l2 pool
  and LRP subnets do not; `TestHostRouteEntriesAreIPv4HostPrefixes`
  asserts the entries are sorted IPv4 `ge 32 le 32` prefixes

`pkg/frr/controller.go:307-312` and `pkg/frr/render.go:235-245` build
the outbound route-map from `advertiseFilter` plus the CIDR of every
`announce: bgp` load balancer pool. The subnets that `nat` type OvnEips
draw from are never merged. A VPC whose EIPs come from a provider-less
pool advertises nothing once one BGP load balancer pool exists and the
BgpConf carries no explicit filter.

Fix: merge the CIDR of every subnet referenced by a `nat` type OvnEip,
as `ge 32 le 32`, next to the pool CIDRs.

## F21 — RouterLBRule still requires an LRP on the pool subnet

**Fixed.** `checkEipAnnouncePath` looks for an `announce: bgp` pool on
the EIP subnet. With one, the rule passes when the VPC redistributes
`lb`, the same `checkBgpAnnouncePath` Services use. Without one the
LRP check stays.

- Commits: `b6524cfd9`
- Tests: `Test_handleAddOrUpdateRouterLBRule` gains a case where an
  EIP from a bgp pool subnet without any LRP creates the service, and
  a case where the same EIP fails with "does not advertise loadbalancer
  vips" when the VPC has no `lb` in `redistribute`

`pkg/controller/router_lb_rule.go:358-366` demands a ready `lrp` EIP
on the EIP's external subnet. F5 lifted that requirement for Services
of type LoadBalancer only (`pkg/controller/service_lb_ovn.go:387-401`).
A RouterLBRule on a provider-less BGP pool therefore fails.

Fix: apply the same split as `checkPoolAnnouncePath`.

## F22 — The default external subnet is never disconnected when extras arrive

**Fixed.** `reconcileVpcExternalSubnetConnections` plans connect and
disconnect from the spec, the status and the presence of the default
gateway LRP in the northbound database. The default subnet is
connected only when no extras are set, and disconnected whenever
extras are set or the LRP is left over from before this fix.

- Commits: `2b1738695`
- Tests: `TestPlanVpcExternalSubnetChanges` covers first enable,
  extras added later, a leftover default LRP next to extras, extras
  removed, steady state and disable;
  `TestReconcileVpcExternalSubnetConnectionsDropsDefaultNextToExtras`
  asserts the patch port and BFD of the default LRP are removed when
  the status already lists the extra subnet;
  `TestReconcileVpcExternalSubnetConnectionsKeepsSteadyState` asserts
  nothing is touched when only the default subnet is wanted and present

`handleUpdateVpcExternal` (`pkg/controller/vpc.go:610-663`) diffs only
`extraExternalSubnets`. The default external subnet is disconnected
only when `enableExternal` flips to false, while the create-time rule
at `vpc.go:629` connects it only when no extras are set. A VPC that
moves from the default external subnet to a transit subnet ends with
two gateway LRPs, and northd then rejects every NAT whose address is
outside both LRP subnets (`northd/en-lr-nat.c:296-316`).

Fix: disconnect the default subnet whenever `extraExternalSubnets` is
non-empty. Until then a migration needs `enableExternal: false`, then
`true` with the new list, which is a per-VPC outage.

## F23 — The BGP next hop is the alphabetically first LRP EIP

**Fixed.** `ValidateVpc` rejects `dynamicRouting.enabled` with more
than one entry in `extraExternalSubnets`. `lrpAddress` takes the LRP
of the single extra subnet when one is set, and otherwise requires
exactly one gateway LRP; a VPC with more is skipped with a warning
instead of advertising through the first name.

- Commits: `e9885ef33`
- Tests: `TestValidateVpc` rejects two extra subnets and accepts one;
  `TestLrpAddressFollowsTheSingleExternalSubnet` asserts the extra
  subnet LRP wins over the default one, the only LRP is used without
  extras, two LRPs without extras are rejected, and a VPC without an
  LRP is rejected

`pkg/frr/controller.go:453-475` picks the next hop for every route of
a VPC by sorting the VPC's `lrp` type OvnEips by name and taking the
first. A VPC with more than one external subnet gets whichever sorts
first, and a transit subnet named after a services subnet advertises
the services LRP as next hop for every EIP.

Fix: reject dynamic routing with more than one external subnet in
`ValidateVpc`, or select the LRP by an explicit field on the Vpc.

## F24 — The LSP sweep deletes ports of the old provider name

**Open.** Found 2026-08-28. Migration only.

`markAndCleanLSP` (`pkg/controller/gc.go:397-412,458-506`) builds its
keep set with the renamed provider suffix. A secondary-NIC port created
before the migration carries the old suffix and is marked on the first
sweep and deleted on the second, six to twelve minutes after the
controller starts, while its pod still runs.

Fix: accept both suffixes in the keep set for one release, or document
`--gc-interval=0` for the first start after a migration and a staged
recreation of affected pods.

## F25 — Cross-VRF leak depends on a shared ASN

**Fixed with F11.** There is no `import vrf default` and no VRF
instance any more, so a peer that re-advertises the /32s of another
chassis puts them only into the default kernel table of the chassis,
never into a VPC table, and per-rack ASNs are safe.

- Commits: `1803192b0`
- Tests: `TestRenderSingleBgpInstance`

`import vrf default` (`pkg/frr/render.go:183`) imports everything the
peer sends into every VRF. A peer that re-advertises the /32s it
learned from one chassis to another is stopped only by AS-path loop
detection, so all chassis must share one ASN. Per-rack ASNs would put
every EIP of the cluster into every VRF as kernel routes, learned
routes and logical flows.

Fix: an inbound route-map that permits only the default route, or no
import at all under the table-direct design of F11.

## F26 — The chart cannot upgrade a kube-ovn release in place

**Open.** Found 2026-08-28 during a real migration.

The fabric chart keeps the `ovs-ovn` DaemonSet and `ovn-central`
Deployment names but changes their selector labels
(`app.kubernetes.io/name: fabric-ovs`, `app.kubernetes.io/part-of:
fabric`). A selector is immutable, so `helm upgrade` over a kube-ovn
release creates every other fabric workload and then fails on those
two. The operator must delete the old objects and run the upgrade
again, and until the new pods are ready the cluster has no
ovn-controller and no OVN databases.

Fix: keep the kube-ovn selector labels on those two workloads, or
document a two-step upgrade in MIGRATION.md with the exact deletion
order (old CNI daemonsets first, then ovn-central, then ovs-ovn,
then helm) and the expected control-plane gap.
