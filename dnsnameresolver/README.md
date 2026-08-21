# DNSNameResolver

This is a CoreDNS plugin that works together with Kube-OVN to implement the [FQDN Egress Firewall](https://network-policy-api.sigs.k8s.io/npeps/npep-133-fqdn-egress-selector/) in [Network Policy API](https://network-policy-api.sigs.k8s.io/).

The origin code comes from [coredns-ocp-dnsnameresolver](https://github.com/openshift/coredns-ocp-dnsnameresolver) with following modifications:

- Change `DNSNameResolver` scope from `namespaced` to `cluster` to better implement Network Policy API.
- Remove DNS to IP mapping, using listers to simplify the logical.
- Remove plugin args.
- Update the build process to use community base images.
- Update dependencies.