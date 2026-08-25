#!/bin/bash
set -eux
export PS4='+ $(date "+%Y-%m-%d %H:%M:%S")\011 '

kubectl delete --ignore-not-found -n kube-system ds fabric-pinger
# ensure fabric-pinger has been deleted
while :; do
  if [ $(kubectl get pod -n kube-system -l app=fabric-pinger -o name | wc -l) -eq 0 ]; then
    break
  fi
  sleep 1
done

for vd in $(kubectl  get vpc-dnses.fabric.cloudyfolks.io -o name); do
  kubectl delete --ignore-not-found $vd
done

for vip in $(kubectl get vips.fabric.cloudyfolks.io -o name); do
   kubectl delete --ignore-not-found $vip
done

for odnat in $(kubectl get ovn-dnat-rules.fabric.cloudyfolks.io -o name); do
   kubectl delete --ignore-not-found $odnat
done

for osnat in $(kubectl get ovn-snat-rules.fabric.cloudyfolks.io -o name); do
   kubectl delete --ignore-not-found $osnat
done

for ofip in $(kubectl get ovn-fips.fabric.cloudyfolks.io -o name); do
   kubectl delete --ignore-not-found $ofip
done

for oeip in $(kubectl get ovn-eips.fabric.cloudyfolks.io -o name); do
   kubectl delete --ignore-not-found $oeip
done

for slr in $(kubectl get switch-lb-rules.fabric.cloudyfolks.io -o name); do
   kubectl delete --ignore-not-found $slr
done

for ippool in $(kubectl get ippools.fabric.cloudyfolks.io -o name); do
  kubectl delete --ignore-not-found $ippool
done

for vlan in $(kubectl get vlans.fabric.cloudyfolks.io -o name); do
  kubectl delete --ignore-not-found $vlan
done

for pn in $(kubectl get provider-networks.fabric.cloudyfolks.io -o name); do
  kubectl delete --ignore-not-found $pn
done

# Delete Kube-OVN components
kubectl delete --ignore-not-found -n kube-system deploy fabric-monitor
kubectl delete --ignore-not-found -n kube-system cm ovn-config ovn-ic-config \
  ovn-external-gw-config ovn-vpc-nat-config
kubectl delete --ignore-not-found -n kube-system svc fabric-pinger fabric-controller fabric-cni fabric-monitor
kubectl delete --ignore-not-found -n kube-system deploy fabric-controller
kubectl delete --ignore-not-found -n kube-system deploy ovn-ic-controller
kubectl delete --ignore-not-found -n kube-system deploy ovn-ic-server

# wait for provider-networks to be deleted before deleting fabric-cni
sleep 5
kubectl delete --ignore-not-found -n kube-system ds fabric-cni

# ensure fabric-cni has been deleted
while :; do
  if [ $(kubectl get pod -n kube-system -l app=fabric-cni -o name | wc -l) -eq 0 ]; then
    break
  fi
  sleep 1
done

for pod in $(kubectl get pod -n kube-system -l app=ovs -o 'jsonpath={.items[?(@.status.phase=="Running")].metadata.name}'); do
  kubectl exec -n kube-system "$pod" -- bash /fabric/uninstall.sh
done

kubectl delete --ignore-not-found svc ovn-nb ovn-sb ovn-northd -n kube-system
kubectl delete --ignore-not-found deploy ovn-central -n kube-system
kubectl delete --ignore-not-found ds ovs-ovn -n kube-system
kubectl delete --ignore-not-found ds ovs-ovn-dpdk -n kube-system
kubectl delete --ignore-not-found secret fabric-tls -n kube-system

# delete vpc-dns content
kubectl delete --ignore-not-found cm vpc-dns-config -n kube-system
kubectl delete --ignore-not-found clusterrole system:vpc-dns
kubectl delete --ignore-not-found clusterrolebinding vpc-dns
kubectl delete --ignore-not-found sa vpc-dns -n kube-system

# remove finalizers
for resource_type in subnets.fabric.cloudyfolks.io vpcs.fabric.cloudyfolks.io ips.fabric.cloudyfolks.io; do
  for resource in $(kubectl get "$resource_type" -o name); do
    kubectl patch "$resource" --type='json' -p '[{"op": "replace", "path": "/metadata/finalizers", "value": []}]'
  done
done

# delete CRD
kubectl delete --ignore-not-found crd \
  security-groups.fabric.cloudyfolks.io \
  ippools.fabric.cloudyfolks.io \
  vpc-egress-gateways.fabric.cloudyfolks.io \
  vlans.fabric.cloudyfolks.io \
  provider-networks.fabric.cloudyfolks.io \
  vips.fabric.cloudyfolks.io \
  switch-lb-rules.fabric.cloudyfolks.io \
  vpc-dnses.fabric.cloudyfolks.io \
  ovn-dnat-rules.fabric.cloudyfolks.io \
  ovn-snat-rules.fabric.cloudyfolks.io \
  ovn-fips.fabric.cloudyfolks.io \
  ovn-eips.fabric.cloudyfolks.io \
  subnets.fabric.cloudyfolks.io \
  vpcs.fabric.cloudyfolks.io \
  ips.fabric.cloudyfolks.io

# Remove annotations/labels in namespaces and nodes
kubectl annotate node --all fabric.cloudyfolks.io/cidr-
kubectl annotate node --all fabric.cloudyfolks.io/gateway-
kubectl annotate node --all fabric.cloudyfolks.io/ip_address-
kubectl annotate node --all fabric.cloudyfolks.io/logical_switch-
kubectl annotate node --all fabric.cloudyfolks.io/mac_address-
kubectl annotate node --all fabric.cloudyfolks.io/port_name-
kubectl annotate node --all fabric.cloudyfolks.io/allocated-
kubectl annotate node --all fabric.cloudyfolks.io/chassis- 
kubectl label node --all fabric/role-

kubectl get node -o name | while read node; do
  kubectl get "$node" -o 'go-template={{ range $k, $v := .metadata.labels }}{{ $k }}{{"\n"}}{{ end }}' | while read label; do
    if echo "$label" | grep -qE '^(.+\.provider-network\.cloudyfolks\.io/(ready|mtu|interface|exclude))$'; then
      kubectl label "$node" "$label-"
    fi
  done
done

kubectl annotate ns --all fabric.cloudyfolks.io/cidr-
kubectl annotate ns --all fabric.cloudyfolks.io/exclude_ips-
kubectl annotate ns --all fabric.cloudyfolks.io/gateway-
kubectl annotate ns --all fabric.cloudyfolks.io/logical_switch-
kubectl annotate ns --all fabric.cloudyfolks.io/private-
kubectl annotate ns --all fabric.cloudyfolks.io/allow-
kubectl annotate ns --all fabric.cloudyfolks.io/allocated-

# ensure fabric components have been deleted
while :; do
  sleep 10
  if [ $(kubectl get pod -n kube-system -l component=network -o name | wc -l) -eq 0 ]; then
    break
  fi
  for pod in $(kubectl -n kube-system get pod -l component=network -o name 2>/dev/null); do
    echo "$pod logs:"
    kubectl -n kube-system logs "$pod" --timestamps --tail 50 2>/dev/null || true
  done
done

# wait for all pods to be deleted before deleting serviceaccount/clusterrole/clusterrolebinding
kubectl delete --ignore-not-found sa ovn ovn-ovs fabric-cni fabric-app -n kube-system
kubectl delete --ignore-not-found clusterrole system:ovn system:ovn-ovs system:fabric-cni system:fabric-app
kubectl delete --ignore-not-found clusterrolebinding ovn ovn ovn-ovs fabric-cni fabric-app
kubectl delete --ignore-not-found rolebinding -n kube-system ovn fabric-cni fabric-app

kubectl delete --ignore-not-found -n kube-system lease fabric-controller
kubectl delete --ignore-not-found -n kube-system secret ovn-ipsec-ca

# Remove annotations in all pods of all namespaces
for ns in $(kubectl get ns -o name | awk -F/ '{print $2}'); do
  echo "annotating pods in namespace $ns"
  kubectl annotate pod --all -n $ns fabric.cloudyfolks.io/cidr-
  kubectl annotate pod --all -n $ns fabric.cloudyfolks.io/gateway-
  kubectl annotate pod --all -n $ns fabric.cloudyfolks.io/ip_address-
  kubectl annotate pod --all -n $ns fabric.cloudyfolks.io/logical_switch-
  kubectl annotate pod --all -n $ns fabric.cloudyfolks.io/mac_address-
  kubectl annotate pod --all -n $ns fabric.cloudyfolks.io/port_name-
  kubectl annotate pod --all -n $ns fabric.cloudyfolks.io/allocated-
  kubectl annotate pod --all -n $ns fabric.cloudyfolks.io/routed-
  kubectl annotate pod --all -n $ns fabric.cloudyfolks.io/vlan_id-
  kubectl annotate pod --all -n $ns fabric.cloudyfolks.io/network_type-
  kubectl annotate pod --all -n $ns fabric.cloudyfolks.io/provider_network-
done
