package controller

import (
	"slices"

	v1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	"k8s.io/client-go/tools/cache"

	fabricv1 "github.com/cloudyfolks-labs/fabric/pkg/apis/fabric/v1"
)

const (
	IndexPodByNode         = "byNodeName"
	IndexEPSByService      = "byServiceName"
	IndexIPBySubnet        = "bySubnet"
	IndexVpcByBFDPort      = "byBFDPort"
	IndexVpcBFDPortEnabled = "enabled"
)

func indexPodByNode(obj any) ([]string, error) {
	pod, ok := obj.(*v1.Pod)
	if !ok || pod.Spec.NodeName == "" {
		return nil, nil
	}
	return []string{pod.Spec.NodeName}, nil
}

func indexEPSByService(obj any) ([]string, error) {
	eps, ok := obj.(*discoveryv1.EndpointSlice)
	if !ok {
		return nil, nil
	}
	svc := getServiceForEndpointSlice(eps)
	if svc == "" {
		return nil, nil
	}
	return []string{eps.Namespace + "/" + svc}, nil
}

func indexVpcByBFDPort(obj any) ([]string, error) {
	vpc, ok := obj.(*fabricv1.Vpc)
	if !ok {
		return nil, nil
	}
	if !vpc.Spec.BFDPort.IsEnabled() {
		return nil, nil
	}
	return []string{IndexVpcBFDPortEnabled}, nil
}

func indexIPBySubnet(obj any) ([]string, error) {
	ip, ok := obj.(*fabricv1.IP)
	if !ok {
		return nil, nil
	}
	subnets := make([]string, 0, 1+len(ip.Spec.AttachSubnets))
	if ip.Spec.Subnet != "" {
		subnets = append(subnets, ip.Spec.Subnet)
	}
	for _, as := range ip.Spec.AttachSubnets {
		if as != "" && !slices.Contains(subnets, as) {
			subnets = append(subnets, as)
		}
	}
	return subnets, nil
}

func (c *Controller) setupIndexers(vpcInformer, podInformer, epsInformer, ipInformer cache.SharedIndexInformer) error {
	if err := vpcInformer.AddIndexers(cache.Indexers{IndexVpcByBFDPort: indexVpcByBFDPort}); err != nil {
		return err
	}
	if err := podInformer.AddIndexers(cache.Indexers{IndexPodByNode: indexPodByNode}); err != nil {
		return err
	}
	if err := epsInformer.AddIndexers(cache.Indexers{IndexEPSByService: indexEPSByService}); err != nil {
		return err
	}
	if err := ipInformer.AddIndexers(cache.Indexers{IndexIPBySubnet: indexIPBySubnet}); err != nil {
		return err
	}
	c.vpcIndexer = vpcInformer.GetIndexer()
	c.podIndexer = podInformer.GetIndexer()
	c.epsIndexer = epsInformer.GetIndexer()
	c.ipIndexer = ipInformer.GetIndexer()
	return nil
}
