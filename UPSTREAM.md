# Upstream

fabric is a fork of [kube-ovn](https://github.com/kubeovn/kube-ovn).
The first commit of this repository imports the full kube-ovn source tree.
The Apache-2.0 license and the upstream copyright headers apply to the
imported code.

- Import base: `507af9be23158026fcd06b28fe02ca3e2933a24d` (kube-ovn master)
- Last synced: `507af9be23158026fcd06b28fe02ca3e2933a24d`

## Why we track master

Bug fixes land on kube-ovn master first. Release branches only receive
cherry-picks. When we track master, we track the fixes.

## Sync procedure

One-time setup:

```
git remote add upstream https://github.com/kubeovn/kube-ovn.git
git config rerere.enabled true
```

For each sync:

```
git fetch upstream
BASE=<Last synced commit from this file>
git diff "$BASE" upstream/master | git apply -3 --index
```

1. Resolve the conflicts. Use `git status` to find them.
2. A conflict in a removed subsystem resolves as a delete (see below).
3. Update the `Last synced` line in this file to the new upstream commit.
4. Commit with message `chore: sync kube-ovn master to <short sha>`.
5. Run the build, `go vet`, and the e2e suites before you push.

Sync every two weeks. Sync immediately for a security fix.

## Removed subsystems

fabric does not carry these upstream subsystems. Upstream changes to
them resolve as a delete during a sync:

- BGP speaker (`pkg/speaker`, `cmd/speaker`, the clab BGP test lab)
- VpcNatGateway family (`VpcNatGateway`, `IptablesEIP`, `IptablesFIPRule`,
  `IptablesDnatRule`, `IptablesSnatRule`, `QoSPolicy` CRDs and their
  controllers, webhooks, and e2e suites)
- Kernel fastpath module (`fastpath/`)
- VpcEgressGateway BGP/EVPN announcer (`EvpnConf` CRD, the FRR sidecar,
  and the `frr-render` command)

The `vpc-nat-gateway` container image remains. The lb-svc feature uses it
as the gateway pod image.

## Renamed identifiers

fabric renamed the user-facing API domains (see `MIGRATION.md` for the
full table): the CRD group is `fabric.cloudyfolks.io`, annotations and
finalizers use `fabric.cloudyfolks.io/<key>`, the annotation templates
use the `.cloudyfolks.io` suffix, and the default provider token is
`fabric` instead of `ovn`. During a sync, upstream changes to these
identifiers resolve to the fabric names. Internal OVN/OVS
`external_ids` (`kube-ovn.io/*`, `vendor=kube-ovn`) are unchanged and
must stay identical to upstream.
