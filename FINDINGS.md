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

- Commits: `2cd25c951`, `7d9ab544e`
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
removes `kubeovn.io/kube-ovn-controller`, `kube-ovn-controller` and
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

Two guards protect a running VPC: a node whose CNI has not published its
chassis yet is skipped instead of failing the reconcile, and the chassis
set is cleared only when no node carries the label at all.

- Commits: `d664a6d7a`, `6c61bf509`, `bf5a779fb`, `a8f8ee128`
- Tests: `Test_UpdateGatewayChassises`,
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

The reload loop no longer exits when a new `router bgp ... vrf ...` line
appears. `frr-reload.py --reload --overwrite` applies a new VRF instance
in the FRR release this chart ships (10.7), so the restart branch had no
purpose and killed PID 1 of the FRR container.

- Commits: `f92bdde06`
- Tests: `TestReloadScriptNeverExits`

Remaining: nothing in this repository proves the reload path against a
running FRR. The dynamic routing e2e covers it indirectly, because a
failover onto a chassis adds a VRF instance there and the suite asserts
the ToR relearns the route without a session reset.

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

- Commits: `e493f608e`
- Tests: `TestNodeApplyState`

**Not done: peer session state.** The design, and why it is not in this
pass:

The agent cannot read the session state today. `kube-ovn-frr` and `frr`
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

The script queried the kube-ovn repository, pushed to Docker Hub,
referenced a sibling docs repository that does not exist and branched on
`master`. `.github/workflows/release.yaml` is now the only release path,
and `docs/release.md` describes it.

`hack/sync-version.sh` copies `VERSION` into the chart image tag, the
chart version, the chart appVersion and the CRD subchart version.
`make sync-version` runs it and `make verify-version` fails when they
drift; the lint job runs `make verify-version`. The chart now says
`v1.17.0`, which matches `VERSION`.

- Commits: `e29054e7d`
- Tests: `make verify-version` in CI

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
