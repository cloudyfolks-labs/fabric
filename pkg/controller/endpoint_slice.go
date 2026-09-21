package controller

import (
	"context"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"time"

	v1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"

	fabricv1 "github.com/cloudyfolks-labs/fabric/pkg/apis/fabric/v1"
	"github.com/cloudyfolks-labs/fabric/pkg/ovs"
	"github.com/cloudyfolks-labs/fabric/pkg/util"
)

type IPPortMapping map[string]string

func getServiceForEndpointSlice(endpointSlice *discoveryv1.EndpointSlice) string {
	if endpointSlice != nil && endpointSlice.Labels != nil {
		return endpointSlice.Labels[discoveryv1.LabelServiceName]
	}

	return ""
}

func findServiceKey(endpointSlice *discoveryv1.EndpointSlice) string {
	service := getServiceForEndpointSlice(endpointSlice)
	if service == "" {
		return ""
	}

	return endpointSlice.Namespace + "/" + service
}

func (c *Controller) enqueueAddEndpointSlice(obj any) {
	if !c.config.EnableLb {
		return
	}

	key := findServiceKey(obj.(*discoveryv1.EndpointSlice))
	if key != "" {
		klog.V(3).Infof("enqueue add endpointSlice %s", key)
		c.addOrUpdateEndpointSliceQueue.Add(key)
		if c.ovnLbSvcEnabled() {
			c.addOrUpdateOvnLbSvcQueue.Add(key)
		}
	}
}

func (c *Controller) enqueueUpdateEndpointSlice(oldObj, newObj any) {
	if !c.config.EnableLb {
		return
	}

	oldEndpointSlice := oldObj.(*discoveryv1.EndpointSlice)
	newEndpointSlice := newObj.(*discoveryv1.EndpointSlice)
	if oldEndpointSlice.ResourceVersion == newEndpointSlice.ResourceVersion {
		return
	}

	if len(oldEndpointSlice.Endpoints) == 0 && len(newEndpointSlice.Endpoints) == 0 {
		return
	}

	if getServiceForEndpointSlice(oldEndpointSlice) == getServiceForEndpointSlice(newEndpointSlice) &&
		reflect.DeepEqual(oldEndpointSlice.Endpoints, newEndpointSlice.Endpoints) &&
		reflect.DeepEqual(oldEndpointSlice.Ports, newEndpointSlice.Ports) {
		return
	}

	key := findServiceKey(newEndpointSlice)
	if key != "" {
		klog.V(3).Infof("enqueue update endpointSlice for service %s", key)
		c.addOrUpdateEndpointSliceQueue.Add(key)
		if c.ovnLbSvcEnabled() {
			c.addOrUpdateOvnLbSvcQueue.Add(key)
		}
	}
}

func (c *Controller) handleUpdateEndpointSlice(key string) error {
	namespace, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("invalid resource key: %s", key))
		return nil
	}

	c.epKeyMutex.LockKey(key)
	defer func() { _ = c.epKeyMutex.UnlockKey(key) }()
	klog.Infof("handle update endpointSlice for service %s", key)

	endpointSlices, err := c.endpointSlicesLister.EndpointSlices(namespace).List(labels.Set{discoveryv1.LabelServiceName: name}.AsSelector())
	if err != nil {
		if errors.IsNotFound(err) {
			return nil
		}
		klog.Error(err)
		return err
	}

	cachedService, err := c.servicesLister.Services(namespace).Get(name)
	if err != nil {
		if errors.IsNotFound(err) {
			return nil
		}
		klog.Error(err)
		return err
	}
	svc := cachedService.DeepCopy()

	var (
		vpcName, subnetName  string
		externalVIPNode      string
		serviceL2StatusReady = true
		ignoreHealthCheck    = true
		isPreferLocalBackend = false
	)

	annotationVips := serviceAnnotationVips(svc)
	lbVips := getVipIps(svc)
	if len(lbVips) == 0 {
		return nil
	}
	for _, ip := range annotationVips {
		if util.CheckProtocol(ip) == fabricv1.ProtocolIPv4 && !serviceHealthChecksDisabled(svc) {
			ignoreHealthCheck = false
		}
	}

	if c.config.EnableLb && c.config.EnableOVNLBPreferLocal {
		if svc.Spec.Type == v1.ServiceTypeLoadBalancer {
			if svc.Spec.ExternalTrafficPolicy == v1.ServiceExternalTrafficPolicyTypeLocal {
				isPreferLocalBackend = true
				externalVIPNode, serviceL2StatusReady, err = c.getServiceL2StatusNode(namespace, name)
				if err != nil {
					return err
				}
			}
		} else if svc.Spec.Type == v1.ServiceTypeClusterIP && svc.Spec.InternalTrafficPolicy != nil && *svc.Spec.InternalTrafficPolicy == v1.ServiceInternalTrafficPolicyLocal {
			isPreferLocalBackend = true
		}
	}

	if c.config.EnableNonPrimaryCNI && serviceHasSelector(svc) {
		var pods []*v1.Pod
		if pods, err = c.podsLister.Pods(namespace).List(labels.Set(svc.Spec.Selector).AsSelector()); err != nil {
			klog.Errorf("failed to get pods for service %s in namespace %s: %v", name, namespace, err)
			return err
		}
		err = c.replaceEndpointAddressesWithSecondaryIPs(endpointSlices, pods)
		if err != nil {
			klog.Errorf("failed to update endpointSlice: %v", err)
			return err
		}
	}

	vpcName, subnetName, err = c.getVpcAndSubnetForEndpoints(endpointSlices, svc)
	if err != nil {
		return err
	}

	var (
		vpc    *fabricv1.Vpc
		svcVpc string
	)

	if vpc, err = c.vpcsLister.Get(vpcName); err != nil {
		klog.Errorf("failed to get vpc %s, %v", vpcName, err)
		return err
	}

	tcpLb, udpLb, sctpLb := vpc.Status.TCPLoadBalancer, vpc.Status.UDPLoadBalancer, vpc.Status.SctpLoadBalancer
	oldTCPLb, oldUDPLb, oldSctpLb := vpc.Status.TCPSessionLoadBalancer, vpc.Status.UDPSessionLoadBalancer, vpc.Status.SctpSessionLoadBalancer
	if svc.Spec.SessionAffinity == v1.ServiceAffinityClientIP {
		tcpLb, udpLb, sctpLb, oldTCPLb, oldUDPLb, oldSctpLb = oldTCPLb, oldUDPLb, oldSctpLb, tcpLb, udpLb, sctpLb
	}
	if c.config.EnableOVNLBPreferLocal {
		if err = c.clearLoadBalancerVIPExternalTrafficLocal(svc, tcpLb, udpLb, sctpLb); err != nil {
			return err
		}
		if err = c.clearLoadBalancerVIPExternalTrafficLocal(svc, oldTCPLb, oldUDPLb, oldSctpLb); err != nil {
			return err
		}
	}

	for _, lbVip := range lbVips {
		skipHealthCheck := ignoreHealthCheck || !slices.Contains(annotationVips, lbVip)
		for _, port := range svc.Spec.Ports {
			var lb, oldLb string
			switch port.Protocol {
			case v1.ProtocolTCP:
				lb, oldLb = tcpLb, oldTCPLb
			case v1.ProtocolUDP:
				lb, oldLb = udpLb, oldUDPLb
			case v1.ProtocolSCTP:
				lb, oldLb = sctpLb, oldSctpLb
			}

			var (
				vip, checkIP             string
				backends                 []string
				ipPortMapping, externals map[string]string
			)

			if !skipHealthCheck {
				if checkIP, err = c.getHealthCheckVip(subnetName, lbVip); err != nil {
					klog.Error(err)
					return err
				}
				externals = map[string]string{
					util.SwitchLBRuleSubnet: subnetName,
				}
			}

			if isPreferLocalBackend {
				checkIP = util.MasqueradeCheckIP
			}

			backends = c.getEndpointBackend(endpointSlices, port, lbVip)

			if !skipHealthCheck || isPreferLocalBackend {
				ipPortMapping, err = c.getIPPortMapping(endpointSlices, svc, checkIP)
				if err != nil {
					err := fmt.Errorf("couldn't get ip port mapping for svc %s/%s: %w", svc.Namespace, svc.Name, err)
					return err
				}
			}

			if len(backends) != 0 {
				vip = util.JoinHostPort(lbVip, port.Port)
				klog.Infof("add vip endpoint %s, backends %v to LB %s", vip, backends, lb)
				if err = c.OVNNbClient.LoadBalancerAddVip(lb, vip, backends...); err != nil {
					klog.Errorf("failed to add vip %s with backends %s to LB %s: %v", lbVip, backends, lb, err)
					return err
				}
				if isPreferLocalBackend &&
					svc.Spec.Type == v1.ServiceTypeLoadBalancer &&
					svc.Spec.ExternalTrafficPolicy == v1.ServiceExternalTrafficPolicyTypeLocal &&
					serviceL2StatusReady &&
					slices.ContainsFunc(svc.Status.LoadBalancer.Ingress, func(ingress v1.LoadBalancerIngress) bool {
						return ingress.IP == lbVip
					}) {
					vipNodeLSP := ""
					if externalVIPNode != "" {
						vipNodeLSP = util.NodeLspName(externalVIPNode)
					}
					if err = c.OVNNbClient.SetLoadBalancerVIPExternalTrafficLocal(lb, vip, vipNodeLSP); err != nil {
						return fmt.Errorf("couldn't mark external local vip %s on LB %s: %w", vip, lb, err)
					}
				}

				if isPreferLocalBackend && len(ipPortMapping) != 0 {
					if err = c.OVNNbClient.LoadBalancerUpdateIPPortMapping(lb, vip, ipPortMapping); err != nil {
						klog.Errorf("failed to update ip port mapping %s for vip %s to LB %s: %v", ipPortMapping, vip, lb, err)
						return err
					}
				}

				if !skipHealthCheck {
					klog.Infof("add health check ip port mapping %v to LB %s", ipPortMapping, lb)
					if err = c.OVNNbClient.LoadBalancerAddHealthCheck(lb, vip, skipHealthCheck, ipPortMapping, externals); err != nil {
						klog.Errorf("failed to add health check for vip %s with ip port mapping %s to LB %s: %v", lbVip, ipPortMapping, lb, err)
						return err
					}
				}
			} else {
				vip = util.JoinHostPort(lbVip, port.Port)
				klog.V(3).Infof("delete vip endpoint %s from LB %s", vip, lb)
				if err = c.OVNNbClient.LoadBalancerDeleteVip(lb, vip, true); err != nil {
					klog.Errorf("failed to delete vip endpoint %s from LB %s: %v", vip, lb, err)
					return err
				}

				klog.V(3).Infof("delete vip endpoint %s from old LB %s", vip, oldLb)
				if err = c.OVNNbClient.LoadBalancerDeleteVip(oldLb, vip, true); err != nil {
					klog.Errorf("failed to delete vip %s from LB %s: %v", vip, oldLb, err)
					return err
				}

				if c.config.EnableOVNLBPreferLocal {
					if err := c.OVNNbClient.LoadBalancerDeleteIPPortMapping(lb, vip); err != nil {
						klog.Errorf("failed to delete ip port mapping for vip %s from LB %s: %v", vip, lb, err)
						return err
					}
					if err := c.OVNNbClient.LoadBalancerDeleteIPPortMapping(oldLb, vip); err != nil {
						klog.Errorf("failed to delete ip port mapping for vip %s from LB %s: %v", vip, lb, err)
						return err
					}
				}
			}
		}
	}

	if svcVpc = svc.Annotations[util.VpcAnnotation]; svcVpc != vpcName {
		patch := util.KVPatch{util.VpcAnnotation: vpcName}
		if err = util.PatchAnnotations(c.config.KubeClient.CoreV1().Services(namespace), svc.Name, patch); err != nil {
			klog.Errorf("failed to patch service %s: %v", key, err)
			return err
		}
	}

	return nil
}

func (c *Controller) replaceEndpointAddressesWithSecondaryIPs(endpointSlices []*discoveryv1.EndpointSlice, pods []*v1.Pod) error {
	processedPods := make(map[string]bool)

	podMap := make(map[string]*v1.Pod, len(pods))
	for i := range pods {
		podMap[pods[i].Name] = pods[i]
	}

	secondaryIPs := make(map[string]string, len(pods))
	for _, pod := range pods {
		providers, err := c.getPodProviders(pod)
		if err != nil {
			return err
		}
		if len(providers) > 0 {
			ipAddress := pod.Annotations[fmt.Sprintf(util.IPAddressAnnotationTemplate, providers[0])]
			if ipAddress != "" {
				secondaryIPs[pod.Name] = ipAddress
			}
		}
	}

	for i, endpoint := range endpointSlices {
		var copiedSlice *discoveryv1.EndpointSlice
		needsUpdate := false

		for j, ep := range endpoint.Endpoints {
			if ep.TargetRef != nil && ep.TargetRef.Kind == util.KindPod {
				podName := ep.TargetRef.Name

				podKey := fmt.Sprintf("%s/%d", podName, i)
				if processedPods[podKey] {
					continue
				}
				if secondaryIP, hasSecondaryIP := secondaryIPs[podName]; hasSecondaryIP {
					if pod, ok := podMap[podName]; ok {
						for k, address := range ep.Addresses {
							if address == pod.Status.PodIP {
								if !needsUpdate {
									copiedSlice = endpoint.DeepCopy()
									needsUpdate = true
								}
								klog.Infof("updating pod %s/%s ip address %s to %s",
									pod.Namespace, pod.Name, pod.Status.PodIP, secondaryIP)
								copiedSlice.Endpoints[j].Addresses[k] = secondaryIP
								processedPods[podKey] = true

								break
							} else if address == secondaryIP {
								processedPods[podKey] = true
								break
							}
						}
					}
				}
			}
		}

		if needsUpdate {
			endpointSlices[i] = copiedSlice
		}
	}

	return nil
}

func (c *Controller) clearLoadBalancerVIPExternalTrafficLocal(svc *v1.Service, tcpLb, udpLb, sctpLb string) error {
	if svc.Spec.Type != v1.ServiceTypeLoadBalancer ||
		svc.Spec.ExternalTrafficPolicy == v1.ServiceExternalTrafficPolicyTypeLocal {
		return nil
	}

	for _, ingress := range svc.Status.LoadBalancer.Ingress {
		if ingress.IP == "" {
			continue
		}
		for _, port := range svc.Spec.Ports {
			var lb string
			switch port.Protocol {
			case v1.ProtocolTCP:
				lb = tcpLb
			case v1.ProtocolUDP:
				lb = udpLb
			case v1.ProtocolSCTP:
				lb = sctpLb
			}
			if lb == "" {
				continue
			}
			vip := util.JoinHostPort(ingress.IP, port.Port)
			if err := c.OVNNbClient.SetLoadBalancerVIPExternalTrafficLocal(lb, vip, ""); err != nil {
				return fmt.Errorf("couldn't clear external local vip marker %s on LB %s: %w", vip, lb, err)
			}
			if err := c.OVNNbClient.LoadBalancerDeleteIPPortMapping(lb, vip); err != nil {
				return fmt.Errorf("couldn't clear external local vip ip port mapping %s on LB %s: %w", vip, lb, err)
			}
		}
	}
	return nil
}

func (c *Controller) enqueueStaticEndpointUpdateInNamespace(namespace string) {
	endpointSlices, err := c.findStaticEndpointSlicesInNamespace(namespace)
	if err != nil {
		err := fmt.Errorf("couldn't find static endpointslices in namespace %s: %w", namespace, err)
		klog.Error(err)
	}

	for _, slice := range endpointSlices {
		c.enqueueAddEndpointSlice(slice)
	}
}

func serviceHealthChecksDisabled(service *v1.Service) bool {
	if service.Annotations != nil && service.Annotations[util.ServiceHealthCheck] == "false" {
		return true
	}

	return false
}

func (c *Controller) findStaticEndpointSlicesInNamespace(namespace string) ([]*discoveryv1.EndpointSlice, error) {
	services, err := c.servicesLister.Services(namespace).List(labels.Everything())
	if err != nil {
		err := fmt.Errorf("couldn't list services in namespace %s: %w", namespace, err)
		klog.Error(err)
		return nil, err
	}

	var filteredServices []*v1.Service
	for _, service := range services {
		if serviceHasSelector(service) {
			continue
		}

		filteredServices = append(filteredServices, service)
	}

	endpointSlices, err := c.findEndpointSlicesForServices(namespace, filteredServices)
	if err != nil {
		return nil, err
	}

	return endpointSlices, nil
}

func (c *Controller) findEndpointSlicesForServices(namespace string, services []*v1.Service) ([]*discoveryv1.EndpointSlice, error) {
	var endpointSlices []*discoveryv1.EndpointSlice

	for _, service := range services {
		objs, err := c.epsIndexer.ByIndex(IndexEPSByService, namespace+"/"+service.Name)
		if err != nil {
			err := fmt.Errorf("couldn't query endpointslices for service %s/%s: %w", namespace, service.Name, err)
			klog.Error(err)
			return nil, err
		}
		for _, obj := range objs {
			endpointSlices = append(endpointSlices, obj.(*discoveryv1.EndpointSlice))
		}
	}

	return endpointSlices, nil
}

func serviceHasSelector(service *v1.Service) bool {
	return len(service.Spec.Selector) > 0
}

func getCustomServiceVpcAndSubnet(service *v1.Service) (vpcName, subnetName string) {
	if service.Annotations != nil {
		vpcName = service.Annotations[util.LogicalRouterAnnotation]
		subnetName = service.Annotations[util.LogicalSwitchAnnotation]
	}

	return vpcName, subnetName
}

func (c *Controller) getDefaultVpcAndSubnet(service *v1.Service, vpcName, subnetName string) (string, string) {
	if vpcName == "" {
		if vpcName = service.Annotations[util.VpcAnnotation]; vpcName == "" {
			vpcName = c.config.ClusterRouter
		}
	}

	if subnetName == "" {
		subnetName = util.DefaultSubnet
	}

	return vpcName, subnetName
}

func (c *Controller) getVpcAndSubnetForEndpoints(endpointSlices []*discoveryv1.EndpointSlice, service *v1.Service) (vpcName, subnetName string, err error) {
	vpcName, subnetName = getCustomServiceVpcAndSubnet(service)
	if vpcName != "" && subnetName != "" {
		return vpcName, subnetName, nil
	}

	if serviceHasSelector(service) {
		vpcName, subnetName = c.findVpcAndSubnetWithTargets(endpointSlices)
	} else {
		pods, err := c.podsLister.Pods(service.Namespace).List(labels.Everything())
		if err != nil {
			err := fmt.Errorf("failed to get pods for service %s in namespace %s: %w", service.Name, service.Namespace, err)
			klog.Error(err)
			return "", "", err
		}

		vpcName, subnetName = c.findVpcAndSubnetWithNoTargets(endpointSlices, pods)
	}

	vpcName, subnetName = c.getDefaultVpcAndSubnet(service, vpcName, subnetName)
	return vpcName, subnetName, nil
}

func (c *Controller) findVpcAndSubnetWithTargets(endpointSlices []*discoveryv1.EndpointSlice) (vpcName, subnetName string) {
	for _, slice := range endpointSlices {
		for _, endpoint := range slice.Endpoints {
			if endpoint.TargetRef == nil {
				continue
			}

			namespace, name := endpoint.TargetRef.Namespace, endpoint.TargetRef.Name
			if name == "" || namespace == "" {
				continue
			}

			pod, err := c.podsLister.Pods(namespace).Get(name)
			if err != nil {
				err := fmt.Errorf("couldn't retrieve pod %s/%s: %w", namespace, name, err)
				klog.Error(err)
				continue
			}

			vpc, subnet, err := c.getEndpointVpcAndSubnet(pod, endpoint.Addresses)
			if err != nil {
				err := fmt.Errorf("couldn't retrieve subnet/vpc for pod %s/%s: %w", namespace, name, err)
				klog.Error(err)
				continue
			}

			if vpcName == "" {
				vpcName = vpc
			}

			if subnetName == "" {
				subnetName = subnet
			}

			if vpcName != "" && subnetName != "" {
				return vpcName, subnetName
			}
		}
	}

	return vpcName, subnetName
}

func (c *Controller) findVpcAndSubnetWithNoTargets(endpointSlices []*discoveryv1.EndpointSlice, pods []*v1.Pod) (vpcName, subnetName string) {
	for _, slice := range endpointSlices {
		for _, endpoint := range slice.Endpoints {
			for _, pod := range pods {
				vpc, subnet, err := c.getEndpointVpcAndSubnet(pod, endpoint.Addresses)
				if err != nil {
					err := fmt.Errorf("couldn't retrieve subnet/vpc for pod %s/%s: %w", pod.Namespace, pod.Name, err)
					klog.Error(err)
					continue
				}

				if vpcName == "" {
					vpcName = vpc
				}

				if subnetName == "" {
					subnetName = subnet
				}

				if vpcName != "" && subnetName != "" {
					return vpcName, subnetName
				}
			}
		}
	}

	return vpcName, subnetName
}

func (c *Controller) getHealthCheckVip(subnetName, lbVip string) (string, error) {
	var (
		needCreateHealthCheckVip bool
		checkVip                 *fabricv1.Vip
		checkIP                  string
		err                      error
	)
	vipName := subnetName
	checkVip, err = c.virtualIpsLister.Get(vipName)
	if err != nil {
		if errors.IsNotFound(err) {
			needCreateHealthCheckVip = true
		} else {
			klog.Errorf("failed to get health check vip %s, %v", vipName, err)
			return "", err
		}
	}
	if needCreateHealthCheckVip {
		vip := &fabricv1.Vip{
			ObjectMeta: metav1.ObjectMeta{
				Name: vipName,
			},
			Spec: fabricv1.VipSpec{
				Subnet: subnetName,
			},
		}
		if _, err = c.config.FabricClient.FabricV1().Vips().Create(context.Background(), vip, metav1.CreateOptions{}); err != nil {
			klog.Errorf("failed to create health check vip %s, %v", vipName, err)
			return "", err
		}

		time.Sleep(1 * time.Second)
		checkVip, err = c.virtualIpsLister.Get(vipName)
		if err != nil {
			klog.Errorf("failed to get health check vip %s, %v", vipName, err)
			return "", err
		}
	}

	if checkVip.Status.V4ip == "" && checkVip.Status.V6ip == "" {
		err = fmt.Errorf("vip %s is not ready", vipName)
		klog.Error(err)
		return "", err
	}

	switch util.CheckProtocol(lbVip) {
	case fabricv1.ProtocolIPv4:
		checkIP = checkVip.Status.V4ip
	case fabricv1.ProtocolIPv6:
		checkIP = checkVip.Status.V6ip
	}
	if checkIP == "" {
		err = fmt.Errorf("failed to get health check vip subnet %s", vipName)
		klog.Error(err)
		return "", err
	}

	return checkIP, nil
}

func (c *Controller) getEndpointBackend(endpointSlices []*discoveryv1.EndpointSlice, servicePort v1.ServicePort, serviceIP string) (backends []string) {
	protocol := util.CheckProtocol(serviceIP)

	for _, endpointSlice := range endpointSlices {
		var targetPort int32
		for _, port := range endpointSlice.Ports {
			if port.Name != nil && *port.Name == servicePort.Name {
				targetPort = *port.Port
				break
			}
		}
		if targetPort == 0 {
			continue
		}

		for _, endpoint := range endpointSlice.Endpoints {
			if !endpointReady(endpoint) {
				continue
			}

			for _, address := range endpoint.Addresses {
				if util.CheckProtocol(address) == protocol {
					backends = append(backends, util.JoinHostPort(address, targetPort))
				}
			}
		}
	}

	return backends
}

func endpointReady(endpoint discoveryv1.Endpoint) bool {
	return endpoint.Conditions.Ready == nil || *endpoint.Conditions.Ready
}

func (c *Controller) addIPPortMappingEntry(pod *v1.Pod, addresses []string, checkVip string, mapping IPPortMapping) error {
	if !pod.DeletionTimestamp.IsZero() {
		return nil
	}

	lspName, err := c.getEndpointTargetLSPName(pod, addresses)
	if err != nil {
		return fmt.Errorf("couldn't get LSP for the endpoint's target: %w", err)
	}

	for _, address := range addresses {
		key := address
		if util.CheckProtocol(address) == fabricv1.ProtocolIPv6 {
			key = fmt.Sprintf("[%s]", address)
		}
		mapping[key] = fmt.Sprintf(util.HealthCheckNamedVipTemplate, lspName, checkVip)
	}

	return nil
}

func (c *Controller) getIPPortMapping(endpointSlices []*discoveryv1.EndpointSlice, service *v1.Service, checkVip string) (IPPortMapping, error) {
	if serviceHasSelector(service) {
		return c.getIPPortMappingWithTargets(endpointSlices, checkVip), nil
	}

	pods, err := c.podsLister.Pods(service.Namespace).List(labels.Everything())
	if err != nil {
		err := fmt.Errorf("failed to get pods for service %s in namespace %s: %w", service.Name, service.Namespace, err)
		klog.Error(err)
		return nil, err
	}

	return c.getIPPortMappingWithNoTargets(endpointSlices, pods, checkVip), nil
}

func (c *Controller) getIPPortMappingWithTargets(endpointSlices []*discoveryv1.EndpointSlice, checkVip string) IPPortMapping {
	mapping := make(IPPortMapping)

	for _, slice := range endpointSlices {
		for _, endpoint := range slice.Endpoints {
			if endpoint.TargetRef == nil {
				continue
			}

			namespace, name := endpoint.TargetRef.Namespace, endpoint.TargetRef.Name
			if name == "" || namespace == "" {
				continue
			}

			pod, err := c.podsLister.Pods(namespace).Get(name)
			if err != nil {
				err := fmt.Errorf("couldn't retrieve pod %s/%s: %w", namespace, name, err)
				klog.Error(err)
				continue
			}

			if err := c.addIPPortMappingEntry(pod, endpoint.Addresses, checkVip, mapping); err != nil {
				err := fmt.Errorf("couldn't compute ip port mapping for pod %s/%s: %w", namespace, name, err)
				klog.Error(err)
				continue
			}
		}
	}

	return mapping
}

func (c *Controller) getIPPortMappingWithNoTargets(endpointSlices []*discoveryv1.EndpointSlice, pods []*v1.Pod, checkVip string) IPPortMapping {
	mapping := make(IPPortMapping)

	for _, slice := range endpointSlices {
		for _, endpoint := range slice.Endpoints {
			for _, pod := range pods {
				provider, err := c.getEndpointProvider(pod, endpoint.Addresses)
				if err != nil {
					err := fmt.Errorf("couldn't get provider for pod %s/%s: %w", pod.Namespace, pod.Name, err)
					klog.Error(err)
					continue
				}

				if provider == "" {
					continue
				}

				if err := c.addIPPortMappingEntry(pod, endpoint.Addresses, checkVip, mapping); err != nil {
					err := fmt.Errorf("couldn't compute ip port mapping for pod %s/%s: %w", pod.Namespace, pod.Name, err)
					klog.Error(err)
					continue
				}
			}
		}
	}

	return mapping
}

func (c *Controller) getPodProviders(pod *v1.Pod) ([]string, error) {
	podNetworks, err := c.getPodFabricNets(pod)
	if err != nil {
		return nil, fmt.Errorf("failed to get pod networks: %w", err)
	}

	var providers []string
	for _, podNetwork := range podNetworks {
		providers = append(providers, podNetwork.ProviderName)
	}

	return providers, nil
}

func getMatchingProviderForAddress(pod *v1.Pod, providers []string, address string) string {
	if pod.Annotations == nil {
		return ""
	}

	for _, provider := range providers {
		ipsForProvider, exists := pod.Annotations[fmt.Sprintf(util.IPAddressAnnotationTemplate, provider)]
		if !exists {
			continue
		}

		ips := strings.Split(ipsForProvider, ",")
		if slices.Contains(ips, address) {
			return provider
		}
	}

	return ""
}

func (c *Controller) getEndpointProvider(pod *v1.Pod, addresses []string) (string, error) {
	providers, err := c.getPodProviders(pod)
	if err != nil {
		return "", err
	}

	var provider string
	for _, address := range addresses {
		if provider = getMatchingProviderForAddress(pod, providers, address); provider != "" {
			return provider, nil
		}
	}

	return "", nil
}

func getEndpointTargetLSPNameFromProvider(pod *v1.Pod, provider string) string {
	if provider == "" {
		provider = util.OvnProvider
	}

	target := pod.Name

	if vmName, exists := pod.Annotations[fmt.Sprintf(util.VMAnnotationTemplate, provider)]; exists {
		target = vmName
	}

	return ovs.PodNameToPortName(target, pod.Namespace, provider)
}

func (c *Controller) getEndpointTargetLSPName(pod *v1.Pod, addresses []string) (string, error) {
	provider, err := c.getEndpointProvider(pod, addresses)
	if err != nil {
		return "", err
	}

	return getEndpointTargetLSPNameFromProvider(pod, provider), nil
}

func getSubnetByProvider(pod *v1.Pod, provider string) (string, error) {
	subnetName, exists := pod.Annotations[fmt.Sprintf(util.LogicalSwitchAnnotationTemplate, provider)]
	if !exists {
		return "", fmt.Errorf("couldn't find subnet linked to provider %s", provider)
	}

	return subnetName, nil
}

func getVpcByProvider(pod *v1.Pod, provider string) string {
	return pod.Annotations[fmt.Sprintf(util.LogicalRouterAnnotationTemplate, provider)]
}

func (c *Controller) getEndpointVpcAndSubnet(pod *v1.Pod, addresses []string) (string, string, error) {
	provider, err := c.getEndpointProvider(pod, addresses)
	if err != nil {
		return "", "", err
	}

	if provider == "" {
		return "", "", nil
	}

	subnet, err := getSubnetByProvider(pod, provider)
	if err != nil {
		return "", "", err
	}

	vpc := getVpcByProvider(pod, provider)

	return vpc, subnet, nil
}
