package controller

import (
	"context"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/retry"
	"k8s.io/klog/v2"
	"k8s.io/utils/set"

	"github.com/ovn-kubernetes/libovsdb/ovsdb"

	kubeovnv1 "github.com/cloudyfolks-labs/fabric/pkg/apis/kubeovn/v1"
	"github.com/cloudyfolks-labs/fabric/pkg/internal"
	"github.com/cloudyfolks-labs/fabric/pkg/util"
)

const (
	ovnLbSvcReadyCondition = "fabric.cloudyfolks.io/LoadBalancerReady"

	reasonOvnLbSvcAllocated              = "Allocated"
	reasonOvnLbSvcAllocationFailed       = "AllocationFailed"
	reasonOvnLbSvcPortConflict           = "PortConflict"
	reasonOvnLbSvcPoolExhausted          = "PoolExhausted"
	reasonOvnLbSvcExternalSubnetNotReady = "ExternalSubnetNotReady"
	reasonOvnLbSvcPoolSelectionFailed    = "PoolSelectionFailed"
	reasonOvnLbSvcTrafficPolicyLocal     = "ExternalTrafficPolicyLocal"
	reasonOvnLbSvcDynamicRoutingNotReady = "DynamicRoutingNotReady"
	reasonOvnLbSvcPoolReady              = "PoolReady"
)

type ovnLbSvcRelease struct {
	key   string
	uid   types.UID
	vpc   string
	vips  []string
	ports []corev1.ServicePort
}

func (c *Controller) ovnLbSvcEnabled() bool {
	return c.config.EnableOvnLbSvc && c.config.EnableLb
}

func isOvnLbSvc(svc *corev1.Service, defaultLoadBalancerClass bool) bool {
	if svc == nil || svc.Spec.Type != corev1.ServiceTypeLoadBalancer {
		return false
	}
	if svc.Spec.LoadBalancerClass == nil {
		return defaultLoadBalancerClass
	}
	return *svc.Spec.LoadBalancerClass == util.LoadBalancerClass
}

func ovnLbSvcEipName(uid types.UID) string {
	return "lb-" + string(uid)
}

func ovnLbSvcLabelValue(namespace, name string) string {
	return namespace + "." + name
}

func parseOvnLbSvcLabelValue(value string) (namespace, name string, ok bool) {
	return strings.Cut(value, ".")
}

func poolAnnounceMode(pool *kubeovnv1.LoadBalancerPool) string {
	if pool.Spec.Announce == "" {
		return kubeovnv1.LoadBalancerPoolAnnounceL2
	}
	return pool.Spec.Announce
}

func protocolOrDefault(protocol corev1.Protocol) corev1.Protocol {
	if protocol == "" {
		return corev1.ProtocolTCP
	}
	return protocol
}

func servicePortsOverlap(a, b []corev1.ServicePort) bool {
	for _, pa := range a {
		for _, pb := range b {
			if pa.Port == pb.Port && protocolOrDefault(pa.Protocol) == protocolOrDefault(pb.Protocol) {
				return true
			}
		}
	}
	return false
}

func splitRequestedLbIPs(annotation, cidrBlock string) (v4, v6 string, err error) {
	for ip := range strings.SplitSeq(annotation, ",") {
		ip = strings.TrimSpace(ip)
		if ip == "" {
			continue
		}
		proto := util.CheckProtocol(ip)
		if proto != kubeovnv1.ProtocolIPv4 && proto != kubeovnv1.ProtocolIPv6 {
			return "", "", fmt.Errorf("invalid requested ip %q", ip)
		}
		if !util.CIDRContainIP(cidrBlock, ip) {
			return "", "", fmt.Errorf("requested ip %s is not in pool subnet cidr %s", ip, cidrBlock)
		}
		switch proto {
		case kubeovnv1.ProtocolIPv4:
			if v4 != "" {
				return "", "", fmt.Errorf("multiple IPv4 addresses requested: %s, %s", v4, ip)
			}
			v4 = ip
		case kubeovnv1.ProtocolIPv6:
			if v6 != "" {
				return "", "", fmt.Errorf("multiple IPv6 addresses requested: %s, %s", v6, ip)
			}
			v6 = ip
		}
	}
	return v4, v6, nil
}

func matchesPoolSelector(pool *kubeovnv1.LoadBalancerPool, svcLabels map[string]string) (bool, error) {
	if pool.Spec.ServiceSelector == nil {
		return true, nil
	}
	selector, err := metav1.LabelSelectorAsSelector(pool.Spec.ServiceSelector)
	if err != nil {
		return false, err
	}
	return selector.Matches(labels.Set(svcLabels)), nil
}

func selectDefaultPool(pools []*kubeovnv1.LoadBalancerPool, svcLabels map[string]string) *kubeovnv1.LoadBalancerPool {
	var selected *kubeovnv1.LoadBalancerPool
	for _, pool := range pools {
		if !pool.Spec.Default {
			continue
		}
		ok, err := matchesPoolSelector(pool, svcLabels)
		if err != nil {
			klog.Errorf("invalid serviceSelector on loadbalancer pool %s: %v", pool.Name, err)
			continue
		}
		if !ok {
			continue
		}
		if selected == nil || pool.Name < selected.Name {
			selected = pool
		}
	}
	return selected
}

func splitAnnotationIPs(annotation string) []string {
	var ips []string
	for ip := range strings.SplitSeq(annotation, ",") {
		if ip = strings.TrimSpace(ip); ip != "" {
			ips = append(ips, ip)
		}
	}
	return ips
}

func ovnEipIPs(eip *kubeovnv1.OvnEip) []string {
	var ips []string
	if eip.Status.V4Ip != "" {
		ips = append(ips, eip.Status.V4Ip)
	}
	if eip.Status.V6Ip != "" {
		ips = append(ips, eip.Status.V6Ip)
	}
	return ips
}

func bigIntToInt64(n internal.BigInt) int64 {
	if !n.IsInt64() {
		return math.MaxInt64
	}
	return n.Int64()
}

func newOvnLbSvcRelease(svc *corev1.Service, defaultVpc string) *ovnLbSvcRelease {
	vpc := svc.Annotations[util.VpcAnnotation]
	if vpc == "" {
		vpc = svc.Annotations[util.LogicalRouterAnnotation]
	}
	if vpc == "" {
		vpc = defaultVpc
	}
	return &ovnLbSvcRelease{
		key:   cache.MetaObjectToName(svc).String(),
		uid:   svc.UID,
		vpc:   vpc,
		vips:  splitAnnotationIPs(svc.Annotations[util.RouterLBRuleVipsAnnotation]),
		ports: svc.Spec.Ports,
	}
}

func (c *Controller) enqueueAddLoadBalancerPool(obj any) {
	pool := obj.(*kubeovnv1.LoadBalancerPool)
	klog.V(3).Infof("enqueue services for new loadbalancer pool %s", pool.Name)
	c.requeueOvnLbServices()
}

func (c *Controller) enqueueUpdateLoadBalancerPool(oldObj, newObj any) {
	oldPool := oldObj.(*kubeovnv1.LoadBalancerPool)
	newPool := newObj.(*kubeovnv1.LoadBalancerPool)
	if oldPool.ResourceVersion == newPool.ResourceVersion ||
		equality.Semantic.DeepEqual(oldPool.Spec, newPool.Spec) {
		return
	}
	klog.V(3).Infof("enqueue services for updated loadbalancer pool %s", newPool.Name)
	c.requeueOvnLbServices()
}

func (c *Controller) enqueueDeleteLoadBalancerPool(_ any) {
	c.requeueOvnLbServices()
}

func (c *Controller) requeueOvnLbServices() {
	svcs, err := c.servicesLister.List(labels.Everything())
	if err != nil {
		klog.Errorf("failed to list services: %v", err)
		return
	}
	for _, svc := range svcs {
		if isOvnLbSvc(svc, c.config.DefaultLoadBalancerClass) {
			c.addOrUpdateOvnLbSvcQueue.Add(cache.MetaObjectToName(svc).String())
		}
	}
}

func (c *Controller) requeueOvnLbSvcForEip(eip *kubeovnv1.OvnEip) {
	if !c.ovnLbSvcEnabled() {
		return
	}
	namespace, name, ok := parseOvnLbSvcLabelValue(eip.Labels[util.LoadBalancerServiceLabel])
	if !ok || namespace == "" || name == "" {
		return
	}
	c.addOrUpdateOvnLbSvcQueue.Add(namespace + "/" + name)
}

func (c *Controller) handleAddOrUpdateOvnLbSvc(key string) error {
	if !c.ovnLbSvcEnabled() {
		return nil
	}
	namespace, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("invalid resource key: %s", key))
		return nil
	}

	c.svcKeyMutex.LockKey(key)
	defer func() { _ = c.svcKeyMutex.UnlockKey(key) }()

	svc, err := c.servicesLister.Services(namespace).Get(name)
	if err != nil {
		if k8serrors.IsNotFound(err) {
			return nil
		}
		klog.Error(err)
		return err
	}

	if !isOvnLbSvc(svc, c.config.DefaultLoadBalancerClass) {
		if _, err = c.ovnEipsLister.Get(ovnLbSvcEipName(svc.UID)); err == nil {
			klog.Infof("service %s lost its ovn loadbalancer claim, releasing", key)
			return c.releaseOvnLbSvc(newOvnLbSvcRelease(svc, c.config.ClusterRouter))
		}
		return nil
	}
	klog.V(3).Infof("handle add/update ovn lb service %s", key)

	pool, err := c.getServiceLbPool(svc)
	if err != nil {
		c.recorder.Event(svc, corev1.EventTypeWarning, reasonOvnLbSvcPoolSelectionFailed, err.Error())
		c.setOvnLbSvcCondition(svc, metav1.ConditionFalse, reasonOvnLbSvcPoolSelectionFailed, err.Error())
		if _, eipErr := c.ovnEipsLister.Get(ovnLbSvcEipName(svc.UID)); eipErr == nil {
			klog.Infof("service %s has no loadbalancer pool any more, releasing", key)
			return c.releaseOvnLbSvc(newOvnLbSvcRelease(svc, c.config.ClusterRouter))
		}
		return nil
	}

	subnet, err := c.subnetsLister.Get(pool.Spec.Subnet)
	if err != nil {
		msg := fmt.Sprintf("subnet %s of loadbalancer pool %s: %v", pool.Spec.Subnet, pool.Name, err)
		c.recorder.Event(svc, corev1.EventTypeWarning, reasonOvnLbSvcExternalSubnetNotReady, msg)
		c.setOvnLbSvcCondition(svc, metav1.ConditionFalse, reasonOvnLbSvcExternalSubnetNotReady, msg)
		return err
	}

	endpointSlices, err := c.endpointSlicesLister.EndpointSlices(namespace).List(
		labels.Set{discoveryv1.LabelServiceName: name}.AsSelector())
	if err != nil {
		klog.Error(err)
		return err
	}
	vpcName, _, err := c.getVpcAndSubnetForEndpoints(endpointSlices, svc)
	if err != nil {
		klog.Error(err)
		return err
	}

	if msg := c.checkPoolAnnouncePath(pool, vpcName); msg != "" {
		reason := reasonOvnLbSvcExternalSubnetNotReady
		if poolAnnounceMode(pool) == kubeovnv1.LoadBalancerPoolAnnounceBGP {
			reason = reasonOvnLbSvcDynamicRoutingNotReady
		}
		c.recorder.Event(svc, corev1.EventTypeWarning, reason, msg)
		c.setOvnLbSvcCondition(svc, metav1.ConditionFalse, reason, msg)
		return fmt.Errorf("%s", msg)
	}

	eip, err := c.ensureOvnLbSvcEip(svc, pool, subnet)
	if err != nil {
		return err
	}
	if eip == nil {
		return nil
	}

	lbIPs := ovnEipIPs(eip)
	if len(lbIPs) == 0 || !eip.Status.Ready {
		return fmt.Errorf("waiting for ovn eip %s of service %s to be ready", eip.Name, key)
	}

	for _, port := range svc.Spec.Ports {
		if err = c.checkEipPortConflict(eip.Name, strconv.Itoa(int(port.Port)), "", ""); err != nil {
			c.recorder.Event(svc, corev1.EventTypeWarning, reasonOvnLbSvcPortConflict, err.Error())
			c.setOvnLbSvcCondition(svc, metav1.ConditionFalse, reasonOvnLbSvcPortConflict, err.Error())
			return err
		}
	}
	if conflictKey := c.findOvnLbSvcPortConflict(svc, lbIPs); conflictKey != "" {
		msg := fmt.Sprintf("ports of service %s overlap with service %s on ip %s", key, conflictKey, strings.Join(lbIPs, ","))
		c.recorder.Event(svc, corev1.EventTypeWarning, reasonOvnLbSvcPortConflict, msg)
		c.setOvnLbSvcCondition(svc, metav1.ConditionFalse, reasonOvnLbSvcPortConflict, msg)
		return fmt.Errorf("%s", msg)
	}

	if err = c.attachVpcLBsToRouter(vpcName); err != nil {
		klog.Error(err)
		return err
	}

	vips := strings.Join(lbIPs, ",")
	if svc.Annotations[util.RouterLBRuleVipsAnnotation] != vips {
		patch := util.KVPatch{util.RouterLBRuleVipsAnnotation: vips}
		if err = util.PatchAnnotations(c.config.KubeClient.CoreV1().Services(namespace), name, patch); err != nil {
			klog.Errorf("failed to patch service %s: %v", key, err)
			return err
		}
		c.addOrUpdateEndpointSliceQueue.Add(key)
	}

	if poolAnnounceMode(pool) == kubeovnv1.LoadBalancerPoolAnnounceL2 {
		lspName := fmt.Sprintf("%s-%s", pool.Spec.Subnet, vpcName)
		if err = c.OVNNbClient.SetLogicalSwitchPortNatAddresses(lspName, "router"); err != nil {
			klog.Errorf("failed to set nat-addresses on lsp %s: %v", lspName, err)
			return err
		}
	}

	return c.updateOvnLbSvcStatus(svc, lbIPs, pool.Name)
}

func vpcAdvertisesLoadBalancerVips(vpc *kubeovnv1.Vpc) bool {
	dr := vpc.Spec.DynamicRouting
	return dr.IsEnabled() && slices.Contains(dr.Redistribute, kubeovnv1.RedistributeLB)
}

func (c *Controller) checkPoolAnnouncePath(pool *kubeovnv1.LoadBalancerPool, vpcName string) string {
	if poolAnnounceMode(pool) == kubeovnv1.LoadBalancerPoolAnnounceBGP {
		vpc, err := c.vpcsLister.Get(vpcName)
		if err != nil {
			return fmt.Sprintf("vpc %s of loadbalancer pool %s: %v", vpcName, pool.Name, err)
		}
		if !vpcAdvertisesLoadBalancerVips(vpc) {
			return fmt.Sprintf("vpc %s does not advertise loadbalancer vips: enable dynamicRouting and add lb to redistribute", vpcName)
		}
		return ""
	}

	lrpEipName := fmt.Sprintf("%s-%s", vpcName, pool.Spec.Subnet)
	lrpEip, err := c.ovnEipsLister.Get(lrpEipName)
	if err != nil || !lrpEip.Status.Ready || lrpEip.Spec.Type != util.OvnEipTypeLRP {
		return fmt.Sprintf("vpc %s has no ready LRP on external subnet %s: ensure the subnet is in the vpc external subnets", vpcName, pool.Spec.Subnet)
	}
	return ""
}

func (c *Controller) getServiceLbPool(svc *corev1.Service) (*kubeovnv1.LoadBalancerPool, error) {
	if poolName := svc.Annotations[util.LoadBalancerPoolAnnotation]; poolName != "" {
		pool, err := c.loadBalancerPoolLister.Get(poolName)
		if err != nil {
			return nil, fmt.Errorf("loadbalancer pool %s: %w", poolName, err)
		}
		return pool, nil
	}
	pools, err := c.loadBalancerPoolLister.List(labels.Everything())
	if err != nil {
		return nil, err
	}
	if pool := selectDefaultPool(pools, svc.Labels); pool != nil {
		return pool, nil
	}
	return nil, fmt.Errorf("no default loadbalancer pool matches service %s/%s", svc.Namespace, svc.Name)
}

func (c *Controller) ensureOvnLbSvcEip(svc *corev1.Service, pool *kubeovnv1.LoadBalancerPool, subnet *kubeovnv1.Subnet) (*kubeovnv1.OvnEip, error) {
	eipName := ovnLbSvcEipName(svc.UID)
	announce := poolAnnounceMode(pool)
	labelValue := ovnLbSvcLabelValue(svc.Namespace, svc.Name)

	if eip, err := c.ovnEipsLister.Get(eipName); err == nil {
		if eip.Spec.ExternalSubnet != pool.Spec.Subnet {
			klog.Infof("loadbalancer pool subnet of service %s/%s changed from %s to %s, releasing eip %s",
				svc.Namespace, svc.Name, eip.Spec.ExternalSubnet, pool.Spec.Subnet, eipName)
			var vipPorts []string
			for _, ip := range ovnEipIPs(eip) {
				for _, port := range svc.Spec.Ports {
					vipPorts = append(vipPorts, util.JoinHostPort(ip, port.Port))
				}
			}
			if err = c.cleanupRouterLBVips(nil, vipPorts); err != nil {
				klog.Error(err)
				return nil, err
			}
			if err = c.config.KubeOvnClient.FabricV1().OvnEips().Delete(context.Background(), eipName, metav1.DeleteOptions{}); err != nil && !k8serrors.IsNotFound(err) {
				klog.Error(err)
				return nil, err
			}
			return nil, fmt.Errorf("recreating ovn eip %s on subnet %s", eipName, pool.Spec.Subnet)
		}
		return c.ensureOvnLbSvcEipLabels(eip, announce, labelValue, c.ovnLbSvcVpc(svc))
	} else if !k8serrors.IsNotFound(err) {
		klog.Error(err)
		return nil, err
	}

	if sharedKey := svc.Annotations[util.LoadBalancerSharedIPAnnotation]; sharedKey != "" {
		donorKey, donorEip, err := c.findOvnLbSvcSharedEip(svc, sharedKey, pool.Spec.Subnet)
		if err != nil {
			c.recorder.Event(svc, corev1.EventTypeWarning, reasonOvnLbSvcPortConflict, err.Error())
			c.setOvnLbSvcCondition(svc, metav1.ConditionFalse, reasonOvnLbSvcPortConflict, err.Error())
			return nil, nil
		}
		if donorEip != nil {
			klog.Infof("service %s/%s shares eip %s with service %s", svc.Namespace, svc.Name, donorEip.Name, donorKey)
			return c.ensureOvnLbSvcEipLabels(donorEip, announce, labelValue, c.ovnLbSvcVpc(svc))
		}
	}

	requestedV4, requestedV6, err := splitRequestedLbIPs(svc.Annotations[util.LoadBalancerIPsAnnotation], subnet.Spec.CIDRBlock)
	if err != nil {
		c.recorder.Event(svc, corev1.EventTypeWarning, reasonOvnLbSvcAllocationFailed, err.Error())
		c.setOvnLbSvcCondition(svc, metav1.ConditionFalse, reasonOvnLbSvcAllocationFailed, err.Error())
		return nil, nil
	}
	if usedBy, err := c.requestedLbIPsInUse(requestedV4, requestedV6); err != nil {
		klog.Error(err)
		return nil, err
	} else if usedBy != "" {
		msg := fmt.Sprintf("requested ip is already in use by ovn eip %s", usedBy)
		c.recorder.Event(svc, corev1.EventTypeWarning, reasonOvnLbSvcAllocationFailed, msg)
		c.setOvnLbSvcCondition(svc, metav1.ConditionFalse, reasonOvnLbSvcAllocationFailed, msg)
		return nil, nil
	}

	if requestedV4 == "" && requestedV6 == "" && poolSubnetExhausted(subnet) {
		msg := fmt.Sprintf("loadbalancer pool %s has no available ip in subnet %s", pool.Name, subnet.Name)
		c.recorder.Event(svc, corev1.EventTypeWarning, reasonOvnLbSvcPoolExhausted, msg)
		c.setOvnLbSvcCondition(svc, metav1.ConditionFalse, reasonOvnLbSvcPoolExhausted, msg)
		return nil, fmt.Errorf("%s", msg)
	}

	eip := &kubeovnv1.OvnEip{
		ObjectMeta: metav1.ObjectMeta{
			Name: eipName,
			Labels: map[string]string{
				util.LoadBalancerAnnounceLabel: announce,
				util.LoadBalancerServiceLabel:  labelValue,
				util.VpcNameLabel:              c.ovnLbSvcVpc(svc),
			},
		},
		Spec: kubeovnv1.OvnEipSpec{
			ExternalSubnet: pool.Spec.Subnet,
			Type:           util.OvnEipTypeNAT,
			V4Ip:           requestedV4,
			V6Ip:           requestedV6,
		},
	}
	klog.Infof("create ovn eip %s for service %s/%s on subnet %s", eipName, svc.Namespace, svc.Name, pool.Spec.Subnet)
	if _, err = c.config.KubeOvnClient.FabricV1().OvnEips().Create(context.Background(), eip, metav1.CreateOptions{}); err != nil && !k8serrors.IsAlreadyExists(err) {
		klog.Error(err)
		return nil, err
	}
	return nil, fmt.Errorf("waiting for ovn eip %s allocation", eipName)
}

func poolSubnetExhausted(subnet *kubeovnv1.Subnet) bool {
	if util.CheckProtocol(subnet.Spec.CIDRBlock) == kubeovnv1.ProtocolIPv6 {
		return subnet.Status.V6AvailableIPs.EqualInt64(0)
	}
	return subnet.Status.V4AvailableIPs.EqualInt64(0)
}

func (c *Controller) requestedLbIPsInUse(requestedV4, requestedV6 string) (string, error) {
	check := func(label, value string) (string, error) {
		eips, err := c.ovnEipsLister.List(labels.SelectorFromSet(labels.Set{label: value}))
		if err != nil {
			return "", err
		}
		if len(eips) > 0 {
			return eips[0].Name, nil
		}
		return "", nil
	}
	if requestedV4 != "" {
		if usedBy, err := check(util.EipV4IpLabel, requestedV4); usedBy != "" || err != nil {
			return usedBy, err
		}
	}
	if requestedV6 != "" {
		if usedBy, err := check(util.EipV6IpLabel, util.IPv6ToLabelValue(requestedV6)); usedBy != "" || err != nil {
			return usedBy, err
		}
	}
	return "", nil
}

func (c *Controller) ensureOvnLbSvcEipLabels(eip *kubeovnv1.OvnEip, announce, serviceLabelValue, vpcName string) (*kubeovnv1.OvnEip, error) {
	patch := util.KVPatch{}
	if eip.Labels[util.LoadBalancerAnnounceLabel] != announce {
		patch[util.LoadBalancerAnnounceLabel] = announce
	}
	if eip.Labels[util.LoadBalancerServiceLabel] == "" {
		patch[util.LoadBalancerServiceLabel] = serviceLabelValue
	}
	if eip.Labels[util.VpcNameLabel] != vpcName {
		patch[util.VpcNameLabel] = vpcName
	}
	if len(patch) > 0 {
		if err := util.PatchLabels(c.config.KubeOvnClient.FabricV1().OvnEips(), eip.Name, patch); err != nil {
			klog.Errorf("failed to patch labels of ovn eip %s: %v", eip.Name, err)
			return nil, err
		}
	}
	return eip, nil
}

func (c *Controller) ovnLbSvcVpc(svc *corev1.Service) string {
	vpc := svc.Annotations[util.VpcAnnotation]
	if vpc == "" {
		vpc = svc.Annotations[util.LogicalRouterAnnotation]
	}
	if vpc == "" {
		vpc = c.config.ClusterRouter
	}
	return vpc
}

func (c *Controller) hasOvnLbSvcInVpc(vpcName, excludeKey string) bool {
	if !c.ovnLbSvcEnabled() {
		return false
	}
	claimed, err := c.claimedOvnLbServices()
	if err != nil {
		klog.Errorf("failed to list services: %v", err)
		return true
	}
	for _, svc := range claimed {
		if cache.MetaObjectToName(svc).String() == excludeKey {
			continue
		}
		if c.ovnLbSvcVpc(svc) == vpcName {
			return true
		}
	}
	return false
}

func (c *Controller) claimedOvnLbServices() ([]*corev1.Service, error) {
	svcs, err := c.servicesLister.List(labels.Everything())
	if err != nil {
		return nil, err
	}
	var claimed []*corev1.Service
	for _, svc := range svcs {
		if isOvnLbSvc(svc, c.config.DefaultLoadBalancerClass) {
			claimed = append(claimed, svc)
		}
	}
	slices.SortFunc(claimed, func(a, b *corev1.Service) int {
		return strings.Compare(cache.MetaObjectToName(a).String(), cache.MetaObjectToName(b).String())
	})
	return claimed, nil
}

func (c *Controller) findOvnLbSvcSharedEip(svc *corev1.Service, sharedKey, poolSubnet string) (string, *kubeovnv1.OvnEip, error) {
	claimed, err := c.claimedOvnLbServices()
	if err != nil {
		return "", nil, err
	}
	for _, cand := range claimed {
		if cand.UID == svc.UID {
			continue
		}
		if cand.Annotations[util.LoadBalancerSharedIPAnnotation] != sharedKey {
			continue
		}
		candEip, err := c.ovnEipsLister.Get(ovnLbSvcEipName(cand.UID))
		if err != nil {
			continue
		}
		if candEip.Spec.ExternalSubnet != poolSubnet {
			continue
		}
		candKey := cache.MetaObjectToName(cand).String()
		if servicePortsOverlap(svc.Spec.Ports, cand.Spec.Ports) {
			return "", nil, fmt.Errorf("cannot share ip with service %s: port/protocol overlap", candKey)
		}
		if conflictKey := c.findOvnLbSvcPortConflict(svc, ovnEipIPs(candEip)); conflictKey != "" {
			return "", nil, fmt.Errorf("cannot share ip with service %s: port/protocol overlap with service %s", candKey, conflictKey)
		}
		return candKey, candEip, nil
	}
	return "", nil, nil
}

func (c *Controller) findOvnLbSvcPortConflict(svc *corev1.Service, ips []string) string {
	claimed, err := c.claimedOvnLbServices()
	if err != nil {
		klog.Errorf("failed to list services: %v", err)
		return ""
	}
	for _, cand := range claimed {
		if cand.UID == svc.UID {
			continue
		}
		candIPs := splitAnnotationIPs(cand.Annotations[util.RouterLBRuleVipsAnnotation])
		shared := slices.ContainsFunc(candIPs, func(ip string) bool { return slices.Contains(ips, ip) })
		if shared && servicePortsOverlap(svc.Spec.Ports, cand.Spec.Ports) {
			return cache.MetaObjectToName(cand).String()
		}
	}
	return ""
}

func (c *Controller) setOvnLbSvcCondition(svc *corev1.Service, status metav1.ConditionStatus, reason, message string) {
	newSvc := svc.DeepCopy()
	meta.SetStatusCondition(&newSvc.Status.Conditions, metav1.Condition{
		Type:               ovnLbSvcReadyCondition,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: svc.Generation,
	})
	if equality.Semantic.DeepEqual(newSvc.Status, svc.Status) {
		return
	}
	if _, err := c.config.KubeClient.CoreV1().Services(svc.Namespace).UpdateStatus(context.Background(), newSvc, metav1.UpdateOptions{}); err != nil {
		klog.Errorf("failed to update status of service %s/%s: %v", svc.Namespace, svc.Name, err)
	}
}

func (c *Controller) updateOvnLbSvcStatus(svc *corev1.Service, lbIPs []string, poolName string) error {
	newSvc := svc.DeepCopy()
	ingress := make([]corev1.LoadBalancerIngress, 0, len(lbIPs))
	for _, ip := range lbIPs {
		ingress = append(ingress, corev1.LoadBalancerIngress{IP: ip})
	}
	newSvc.Status.LoadBalancer.Ingress = ingress
	meta.SetStatusCondition(&newSvc.Status.Conditions, metav1.Condition{
		Type:               ovnLbSvcReadyCondition,
		Status:             metav1.ConditionTrue,
		Reason:             reasonOvnLbSvcAllocated,
		Message:            fmt.Sprintf("assigned %s from pool %s", strings.Join(lbIPs, ","), poolName),
		ObservedGeneration: svc.Generation,
	})
	if equality.Semantic.DeepEqual(newSvc.Status, svc.Status) {
		return nil
	}
	if _, err := c.config.KubeClient.CoreV1().Services(svc.Namespace).UpdateStatus(context.Background(), newSvc, metav1.UpdateOptions{}); err != nil {
		klog.Errorf("failed to update status of service %s/%s: %v", svc.Namespace, svc.Name, err)
		return err
	}
	if !equality.Semantic.DeepEqual(newSvc.Status.LoadBalancer, svc.Status.LoadBalancer) {
		c.recorder.Eventf(svc, corev1.EventTypeNormal, reasonOvnLbSvcAllocated, "assigned ip %s from pool %s", strings.Join(lbIPs, ","), poolName)
		if svc.Spec.ExternalTrafficPolicy == corev1.ServiceExternalTrafficPolicyTypeLocal {
			c.recorder.Event(svc, corev1.EventTypeNormal, reasonOvnLbSvcTrafficPolicyLocal,
				"externalTrafficPolicy Local behaves as Cluster with preserved client source ip")
		}
	}
	return nil
}

func (c *Controller) handleDelOvnLbSvc(rel *ovnLbSvcRelease) error {
	if !c.ovnLbSvcEnabled() {
		return nil
	}

	c.svcKeyMutex.LockKey(rel.key)
	defer func() { _ = c.svcKeyMutex.UnlockKey(rel.key) }()

	return c.releaseOvnLbSvc(rel)
}

func (c *Controller) releaseOvnLbSvc(rel *ovnLbSvcRelease) error {
	namespace, name, err := cache.SplitMetaNamespaceKey(rel.key)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("invalid resource key: %s", rel.key))
		return nil
	}

	if svc, err := c.servicesLister.Services(namespace).Get(name); err == nil {
		if isOvnLbSvc(svc, c.config.DefaultLoadBalancerClass) && svc.UID == rel.uid {
			return nil
		}
	} else if !k8serrors.IsNotFound(err) {
		klog.Error(err)
		return err
	}
	klog.Infof("release ovn lb service %s", rel.key)

	remaining, err := c.remainingOvnLbSvcClaimants(rel.key, rel.vips)
	if err != nil {
		klog.Error(err)
		return err
	}

	vipPorts := make([]string, 0, len(rel.vips)*len(rel.ports))
	for _, ip := range rel.vips {
		for _, port := range rel.ports {
			vipPorts = append(vipPorts, util.JoinHostPort(ip, port.Port))
		}
	}

	var vpcLBNames set.Set[string]
	vpc, err := c.vpcsLister.Get(rel.vpc)
	switch {
	case err == nil:
		vpcLBNames = set.New(
			vpc.Status.TCPLoadBalancer, vpc.Status.UDPLoadBalancer,
			vpc.Status.SctpLoadBalancer, vpc.Status.TCPSessionLoadBalancer,
			vpc.Status.UDPSessionLoadBalancer, vpc.Status.SctpSessionLoadBalancer,
		)
		vpcLBNames.Delete("")
	case k8serrors.IsNotFound(err):
		klog.Warningf("vpc %s not found for service %s, falling back to unscoped cleanup", rel.vpc, rel.key)
	default:
		klog.Error(err)
		return err
	}

	if err = c.cleanupRouterLBVips(vpcLBNames, vipPorts); err != nil {
		klog.Error(err)
		return err
	}

	if len(remaining) == 0 {
		if err = c.deleteOvnLbSvcEips(rel); err != nil {
			klog.Error(err)
			return err
		}
		if err = c.detachVpcLBsFromRouterIfUnused(rel.vpc, rel.key, vpcLBNames); err != nil {
			klog.Error(err)
			return err
		}
	} else if eip, err := c.ovnEipsLister.Get(ovnLbSvcEipName(rel.uid)); err == nil {
		if eip.Labels[util.LoadBalancerServiceLabel] == ovnLbSvcLabelValue(namespace, name) {
			ns, svcName, _ := cache.SplitMetaNamespaceKey(remaining[0])
			patch := util.KVPatch{util.LoadBalancerServiceLabel: ovnLbSvcLabelValue(ns, svcName)}
			if err = util.PatchLabels(c.config.KubeOvnClient.FabricV1().OvnEips(), eip.Name, patch); err != nil {
				klog.Errorf("failed to patch labels of ovn eip %s: %v", eip.Name, err)
				return err
			}
		}
	} else if !k8serrors.IsNotFound(err) {
		klog.Error(err)
		return err
	}

	if svc, err := c.servicesLister.Services(namespace).Get(name); err == nil {
		if svc.Annotations[util.RouterLBRuleVipsAnnotation] != "" {
			patch := util.KVPatch{util.RouterLBRuleVipsAnnotation: nil}
			if err = util.PatchAnnotations(c.config.KubeClient.CoreV1().Services(namespace), name, patch); err != nil {
				klog.Errorf("failed to patch service %s: %v", rel.key, err)
				return err
			}
		}
		newSvc := svc.DeepCopy()
		newSvc.Status.LoadBalancer.Ingress = nil
		meta.RemoveStatusCondition(&newSvc.Status.Conditions, ovnLbSvcReadyCondition)
		if !equality.Semantic.DeepEqual(newSvc.Status, svc.Status) {
			if _, err = c.config.KubeClient.CoreV1().Services(namespace).UpdateStatus(context.Background(), newSvc, metav1.UpdateOptions{}); err != nil {
				klog.Errorf("failed to update status of service %s: %v", rel.key, err)
				return err
			}
		}
	} else if !k8serrors.IsNotFound(err) {
		klog.Error(err)
		return err
	}

	return nil
}

func (c *Controller) detachVpcLBsFromRouterIfUnused(vpcName, excludeKey string, vpcLBNames set.Set[string]) error {
	if vpcLBNames == nil || vpcLBNames.Len() == 0 || c.hasOvnLbSvcInVpc(vpcName, excludeKey) {
		return nil
	}
	rlrs, err := c.routerLBRuleLister.List(labels.Everything())
	if err != nil {
		return err
	}
	if slices.ContainsFunc(rlrs, func(r *kubeovnv1.RouterLBRule) bool { return r.Spec.Vpc == vpcName }) {
		return nil
	}
	klog.Infof("detach LBs from router %s after last ovn lb service released", vpcName)
	return c.OVNNbClient.LogicalRouterUpdateLoadBalancers(vpcName, ovsdb.MutateOperationDelete, vpcLBNames.UnsortedList()...)
}

func (c *Controller) deleteOvnLbSvcEips(rel *ovnLbSvcRelease) error {
	if err := c.config.KubeOvnClient.FabricV1().OvnEips().Delete(context.Background(), ovnLbSvcEipName(rel.uid), metav1.DeleteOptions{}); err != nil && !k8serrors.IsNotFound(err) {
		return err
	}
	requirement, err := labels.NewRequirement(util.LoadBalancerServiceLabel, selection.Exists, nil)
	if err != nil {
		return err
	}
	eips, err := c.ovnEipsLister.List(labels.NewSelector().Add(*requirement))
	if err != nil {
		return err
	}
	for _, eip := range eips {
		if !slices.ContainsFunc(ovnEipIPs(eip), func(ip string) bool { return slices.Contains(rel.vips, ip) }) {
			continue
		}
		klog.Infof("delete orphaned ovn eip %s of released service %s", eip.Name, rel.key)
		if err = c.config.KubeOvnClient.FabricV1().OvnEips().Delete(context.Background(), eip.Name, metav1.DeleteOptions{}); err != nil && !k8serrors.IsNotFound(err) {
			return err
		}
	}
	return nil
}

func (c *Controller) remainingOvnLbSvcClaimants(excludeKey string, ips []string) ([]string, error) {
	claimed, err := c.claimedOvnLbServices()
	if err != nil {
		return nil, err
	}
	var remaining []string
	for _, cand := range claimed {
		candKey := cache.MetaObjectToName(cand).String()
		if candKey == excludeKey {
			continue
		}
		candIPs := splitAnnotationIPs(cand.Annotations[util.RouterLBRuleVipsAnnotation])
		if slices.ContainsFunc(candIPs, func(ip string) bool { return slices.Contains(ips, ip) }) {
			remaining = append(remaining, candKey)
		}
	}
	return remaining, nil
}

func (c *Controller) resyncLoadBalancerPoolStatus() {
	if !c.ovnLbSvcEnabled() {
		return
	}
	pools, err := c.loadBalancerPoolLister.List(labels.Everything())
	if err != nil {
		klog.Errorf("failed to list loadbalancer pools: %v", err)
		return
	}
	for _, pool := range pools {
		if err = c.updateLoadBalancerPoolUsage(pool); err != nil {
			klog.Errorf("failed to update status of loadbalancer pool %s: %v", pool.Name, err)
		}
	}
}

func loadBalancerPoolReadiness(subnetFound bool, available int64) (corev1.ConditionStatus, string, string) {
	if !subnetFound {
		return corev1.ConditionFalse, reasonOvnLbSvcExternalSubnetNotReady, "subnet does not exist"
	}
	if available == 0 {
		return corev1.ConditionFalse, reasonOvnLbSvcPoolExhausted, "subnet has no available address"
	}
	return corev1.ConditionTrue, reasonOvnLbSvcPoolReady, "pool is ready"
}

func (c *Controller) updateLoadBalancerPoolUsage(pool *kubeovnv1.LoadBalancerPool) error {
	var available int64
	subnetFound := true
	subnet, err := c.subnetsLister.Get(pool.Spec.Subnet)
	switch {
	case err == nil:
		if util.CheckProtocol(subnet.Spec.CIDRBlock) == kubeovnv1.ProtocolIPv6 {
			available = bigIntToInt64(subnet.Status.V6AvailableIPs)
		} else {
			available = bigIntToInt64(subnet.Status.V4AvailableIPs)
		}
	case k8serrors.IsNotFound(err):
		subnetFound = false
	default:
		return err
	}

	eips, err := c.ovnEipsLister.List(labels.SelectorFromSet(labels.Set{util.SubnetNameLabel: pool.Spec.Subnet}))
	if err != nil {
		return err
	}
	var inUse int64
	for _, eip := range eips {
		if eip.Labels[util.LoadBalancerServiceLabel] != "" {
			inUse++
		}
	}

	status, reason, message := loadBalancerPoolReadiness(subnetFound, available)
	pools := c.config.KubeOvnClient.FabricV1().LoadBalancerPools()
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		current, err := pools.Get(context.Background(), pool.Name, metav1.GetOptions{})
		if err != nil {
			if k8serrors.IsNotFound(err) {
				return nil
			}
			return err
		}
		newPool := current.DeepCopy()
		newPool.Status.Available = available
		newPool.Status.InUse = inUse
		conditions := kubeovnv1.Conditions(newPool.Status.Conditions)
		conditions.SetCondition(kubeovnv1.Ready, status, reason, message, newPool.Generation)
		newPool.Status.Conditions = conditions
		if equality.Semantic.DeepEqual(newPool.Status, current.Status) {
			return nil
		}
		_, err = pools.UpdateStatus(context.Background(), newPool, metav1.UpdateOptions{})
		return err
	})
}
