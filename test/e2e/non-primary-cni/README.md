# Non-Primary CNI E2E Tests

This directory contains the end-to-end tests for fabric in non-primary CNI mode. In this mode a different CNI plugin is the primary CNI, and fabric attaches secondary interfaces through Multus.

## KIND Bridge Network Detection

The suite inspects the KIND Docker network with `docker.NetworkInspect(kind.NetworkName)`. It writes the detected CIDR and gateway into a temporary copy of each test configuration, runs the tests with that copy, and removes the copy when the tests complete. The suite works with each Docker bridge CIDR that KIND uses (172.17.0.0/16, 172.18.0.0/16, 172.19.0.0/16, and others).

## Test Scenarios

### 1. VPC Simple

- Purpose: basic non-primary CNI operation in VPC mode
- Coverage: pod creation, network attachment, IP assignment, pod-to-pod connectivity
- Interface: `net1`
- Config: `testconfigs/VPC/00-vpc-simple.yaml`

### 2. Logical Network Simple

- Purpose: non-primary CNI operation with an isolated logical network
- Coverage: network isolation, pod-to-pod connectivity, logical switches
- Interface: `net1`
- Config: `testconfigs/LogicalNetwork/00-lnet-simple.yaml`

### 3. Host Rules

- Purpose: make sure that fabric does not install host iptables chains or rules in non-primary CNI mode
- Coverage: the `OVN-*` chains in the `nat`, `filter` and `mangle` tables, the jump rules in `PREROUTING` and `POSTROUTING`, and the NodePort and service `MARK` rules

## Prerequisites

1. A KIND cluster with Multus installed
2. A different CNI plugin (for example Flannel or Calico) as the primary CNI
3. fabric installed in non-primary CNI mode (`KUBE_OVN_PRIMARY_CNI=false`)
4. The test configuration files in the `TEST_CONFIG_PATH` directory

## Configuration Files

The default configuration directory is `test/e2e/non-primary-cni/testconfigs`. Set `TEST_CONFIG_PATH` to use a different directory.

```
testconfigs/
├── VPC/
│   └── 00-vpc-simple.yaml
└── LogicalNetwork/
    └── 00-lnet-simple.yaml
```

Each configuration file contains the NetworkAttachmentDefinition and the other resources of one scenario. The `config-stage` label sets the order in which the suite applies the resources:

- Stage 0: infrastructure (VPCs, subnets, network attachment definitions)
- Stage 1: test pods

## Running Tests

```bash
make fabric-non-primary-cni-e2e
```

## Environment Variables

- `E2E_BRANCH`: the git branch under test
- `E2E_IP_FAMILY`: the IP family of the cluster (`ipv4`, `ipv6` or `dual`)
- `E2E_NETWORK_MODE`: the network mode (`overlay` or `underlay`)
- `TEST_CONFIG_PATH`: the directory with the test configuration files
- `KUBE_OVN_PRIMARY_CNI`: must be `false` for these tests

## Interface Configuration

The tests read the interface name and the IP address of each pod from the network status annotation. The default interface is `net1` (the `DefaultNetworkInterface` constant).

These tests run only on KIND clusters.

## Test Structure

The tests use the Ginkgo framework:

1. Setup: initialize the Kubernetes clients and load the test configuration
2. Staged deployment: apply the resources in the order of the `config-stage` labels
3. IP discovery: read the pod IP addresses from the network status annotations
4. Connectivity: test the connectivity between the pods on the configured interface
5. Validation: check the network attachments, the IP assignments and the connectivity
6. Cleanup: remove the test resources

## Debugging

Run one scenario with a Ginkgo focus pattern:

```bash
ginkgo --focus="logical network" ./test/e2e/non-primary-cni/
```

Read the fabric logs with `kubectl logs -n kube-system <pod-name>` and the resource state with `kubectl describe`.

## Common Issues

1. Missing NetworkAttachmentDefinition: make sure that the NAD is in the correct namespace
2. IP assignment failures: check the IPAM configuration and the available IP ranges
3. Connectivity issues: check the OVN/OVS configuration and the network policies
4. Test configuration not found: make sure that the files exist in `TEST_CONFIG_PATH`
5. Interface not found: make sure that the interface names match the test configuration
6. Network status annotation missing: make sure that the pods have the network status annotation from the CNI
