package controller

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	nadv1 "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/apis/k8s.cni.cncf.io/v1"
	nadutils "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/utils"
	"github.com/scylladb/go-set/strset"
	multustypes "gopkg.in/k8snetworkplumbingwg/multus-cni.v4/pkg/types"
	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"
	kubevirtv1 "kubevirt.io/api/core/v1"

	fabricv1 "github.com/cloudyfolks-labs/fabric/pkg/apis/fabric/v1"
	"github.com/cloudyfolks-labs/fabric/pkg/ipam"
	"github.com/cloudyfolks-labs/fabric/pkg/ovs"
	"github.com/cloudyfolks-labs/fabric/pkg/ovsdb/ovnnb"
	"github.com/cloudyfolks-labs/fabric/pkg/util"
)

type NamedPort struct {
	mutex sync.RWMutex

	namedPortMap map[string]map[string]*util.NamedPortInfo
}

func NewNamedPort() *NamedPort {
	return &NamedPort{
		mutex:        sync.RWMutex{},
		namedPortMap: map[string]map[string]*util.NamedPortInfo{},
	}
}

func (n *NamedPort) AddNamedPortByPod(pod *v1.Pod) {
	n.mutex.Lock()
	defer n.mutex.Unlock()
	ns := pod.Namespace
	podName := pod.Name

	restartableInitContainers := make([]v1.Container, 0, len(pod.Spec.InitContainers))
	for i := range pod.Spec.InitContainers {
		if pod.Spec.InitContainers[i].RestartPolicy != nil &&
			*pod.Spec.InitContainers[i].RestartPolicy == v1.ContainerRestartPolicyAlways {
			restartableInitContainers = append(restartableInitContainers, pod.Spec.InitContainers[i])
		}
	}

	containers := slices.Concat(restartableInitContainers, pod.Spec.Containers)
	if len(containers) == 0 {
		return
	}

	for _, container := range containers {
		if len(container.Ports) == 0 {
			continue
		}

		for _, port := range container.Ports {
			if port.Name == "" || port.ContainerPort == 0 {
				continue
			}

			if _, ok := n.namedPortMap[ns]; ok {
				if _, ok := n.namedPortMap[ns][port.Name]; ok {
					if n.namedPortMap[ns][port.Name].PortID == port.ContainerPort {
						n.namedPortMap[ns][port.Name].Pods.Add(podName)
					} else {
						klog.Warningf("named port %s has already been defined with portID %d",
							port.Name, n.namedPortMap[ns][port.Name].PortID)
					}
					continue
				}
			} else {
				n.namedPortMap[ns] = make(map[string]*util.NamedPortInfo)
			}
			n.namedPortMap[ns][port.Name] = &util.NamedPortInfo{
				PortID: port.ContainerPort,
				Pods:   strset.New(podName),
			}
		}
	}
}

func (n *NamedPort) DeleteNamedPortByPod(pod *v1.Pod) {
	n.mutex.Lock()
	defer n.mutex.Unlock()

	ns := pod.Namespace
	podName := pod.Name

	restartableInitContainers := make([]v1.Container, 0, len(pod.Spec.InitContainers))
	for i := range pod.Spec.InitContainers {
		if pod.Spec.InitContainers[i].RestartPolicy != nil &&
			*pod.Spec.InitContainers[i].RestartPolicy == v1.ContainerRestartPolicyAlways {
			restartableInitContainers = append(restartableInitContainers, pod.Spec.InitContainers[i])
		}
	}

	containers := slices.Concat(restartableInitContainers, pod.Spec.Containers)
	if len(containers) == 0 {
		return
	}

	for _, container := range containers {
		if len(container.Ports) == 0 {
			continue
		}

		for _, port := range container.Ports {
			if port.Name == "" {
				continue
			}

			if _, ok := n.namedPortMap[ns]; !ok {
				continue
			}

			if _, ok := n.namedPortMap[ns][port.Name]; !ok {
				continue
			}

			if !n.namedPortMap[ns][port.Name].Pods.Has(podName) {
				continue
			}

			n.namedPortMap[ns][port.Name].Pods.Remove(podName)
			if n.namedPortMap[ns][port.Name].Pods.Size() == 0 {
				delete(n.namedPortMap[ns], port.Name)
				if len(n.namedPortMap[ns]) == 0 {
					delete(n.namedPortMap, ns)
				}
			}
		}
	}
}

func (n *NamedPort) GetNamedPortByNs(namespace string) map[string]*util.NamedPortInfo {
	n.mutex.RLock()
	defer n.mutex.RUnlock()

	if result, ok := n.namedPortMap[namespace]; ok {
		klog.V(3).Infof("namespace %s has %d named ports", namespace, len(result))
		return maps.Clone(result)
	}
	return nil
}

func isPodAlive(p *v1.Pod) bool {
	if !p.DeletionTimestamp.IsZero() && p.DeletionGracePeriodSeconds != nil {
		now := time.Now()
		deletionTime := p.DeletionTimestamp.Time
		gracePeriod := time.Duration(*p.DeletionGracePeriodSeconds) * time.Second
		if now.After(deletionTime.Add(gracePeriod)) {
			return false
		}
	}
	return isPodStatusPhaseAlive(p)
}

func isPodStatusPhaseAlive(p *v1.Pod) bool {
	if p.Status.Phase == v1.PodSucceeded && p.Spec.RestartPolicy != v1.RestartPolicyAlways {
		return false
	}

	if p.Status.Phase == v1.PodFailed && p.Spec.RestartPolicy == v1.RestartPolicyNever {
		return false
	}

	if p.Status.Phase == v1.PodFailed && p.Status.Reason == "Evicted" {
		return false
	}
	return true
}

func (c *Controller) enqueueAddPod(obj any) {
	p := obj.(*v1.Pod)
	if p.Spec.HostNetwork {
		return
	}

	c.enqueueStaticEndpointUpdateInNamespace(p.Namespace)

	if c.config.EnableNP {
		c.namedPort.AddNamedPortByPod(p)
		if p.Status.PodIP != "" {
			for _, np := range c.podMatchNetworkPolicies(p) {
				klog.V(3).Infof("enqueue update network policy %s", np)
				c.updateNpQueue.Add(np)
			}
		}
	}

	key := cache.MetaObjectToName(p).String()
	if !isPodAlive(p) {
		isStateful, statefulSetName, statefulSetUID := isStatefulSetPod(p)
		isVMPod, vmName := isVMPod(p)
		if isStateful || (isVMPod && c.config.EnableKeepVMIP) {
			if isStateful && isStatefulSetPodToDel(c.config.KubeClient, p, statefulSetName, statefulSetUID) {
				klog.V(3).Infof("enqueue delete pod %s", key)
				c.deletingPodObjMap.Store(key, p)
				c.deletePodQueue.Add(key)
			}
			if isVMPod && c.isVMToDel(p, vmName) {
				klog.V(3).Infof("enqueue delete pod %s", key)
				c.deletingPodObjMap.Store(key, p)
				c.deletePodQueue.Add(key)
			}
		} else {
			klog.V(3).Infof("enqueue delete pod %s", key)
			c.deletingPodObjMap.Store(key, p)
			c.deletePodQueue.Add(key)
		}
		return
	}

	need, err := c.podNeedSync(p)
	if err != nil {
		klog.Errorf("invalid pod net: %v", err)
		return
	}
	if need {
		klog.Infof("enqueue add pod %s", key)
		c.addOrUpdatePodQueue.Add(key)
	}
}

func (c *Controller) getNsLabels(nsName, podName string) map[string]string {
	podNs, err := c.namespacesLister.Get(nsName)
	if err != nil {
		if k8serrors.IsNotFound(err) {
			klog.V(3).Infof("namespace %s not found for pod %s, use empty ns labels", nsName, podName)
		} else {
			klog.Errorf("failed to get namespace %s: %v, use empty ns labels", nsName, err)
		}
		return nil
	}
	return podNs.Labels
}

func (c *Controller) enqueueDeletePod(obj any) {
	var p *v1.Pod
	switch t := obj.(type) {
	case *v1.Pod:
		p = t
	case cache.DeletedFinalStateUnknown:
		pod, ok := t.Obj.(*v1.Pod)
		if !ok {
			klog.Warningf("unexpected object type: %T", t.Obj)
			return
		}
		p = pod
	default:
		klog.Warningf("unexpected type: %T", obj)
		return
	}

	if p.Spec.HostNetwork {
		return
	}

	c.enqueueStaticEndpointUpdateInNamespace(p.Namespace)

	if c.config.EnableNP {
		c.namedPort.DeleteNamedPortByPod(p)
		for _, np := range c.podMatchNetworkPolicies(p) {
			c.updateNpQueue.Add(np)
		}
	}

	if c.config.EnableANP {
		nsLabels := c.getNsLabels(p.Namespace, p.Name)
		c.updateAnpsByLabelsMatch(nsLabels, p.Labels)
		c.updateCnpsByLabelsMatch(nsLabels, p.Labels)
	}

	key := cache.MetaObjectToName(p).String()
	klog.Infof("enqueue delete pod %s", key)
	c.deletingPodObjMap.Store(key, p)
	c.deletePodQueue.Add(key)
}

func (c *Controller) enqueueUpdatePod(oldObj, newObj any) {
	oldPod := oldObj.(*v1.Pod)
	newPod := newObj.(*v1.Pod)

	c.enqueueStaticEndpointUpdateInNamespace(oldPod.Namespace)

	if oldPod.Annotations[util.AAPsAnnotation] != "" || newPod.Annotations[util.AAPsAnnotation] != "" {
		oldAAPs := strings.Split(oldPod.Annotations[util.AAPsAnnotation], ",")
		newAAPs := strings.Split(newPod.Annotations[util.AAPsAnnotation], ",")
		var vipNames []string
		for _, vipName := range oldAAPs {
			vipNames = append(vipNames, strings.TrimSpace(vipName))
		}
		for _, vipName := range newAAPs {
			vipName = strings.TrimSpace(vipName)
			if !slices.Contains(vipNames, vipName) {
				vipNames = append(vipNames, vipName)
			}
		}
		for _, vipName := range vipNames {
			if vip, err := c.virtualIpsLister.Get(vipName); err == nil {
				if vip.Spec.Namespace != newPod.Namespace {
					continue
				}
				klog.Infof("enqueue update virtual parents for %s", vipName)
				c.updateVirtualParentsQueue.Add(vipName)
			}
		}
	}

	if newPod.Spec.HostNetwork || oldPod.ResourceVersion == newPod.ResourceVersion {
		return
	}

	podNets, err := c.getPodFabricNets(newPod)
	if err != nil {
		klog.Errorf("failed to get newPod nets %v", err)
		c.recorder.Eventf(newPod, v1.EventTypeWarning, "PodNetworkUpdateFailed", "stage=getPodFabricNets error=%v", err)
		return
	}

	key := cache.MetaObjectToName(newPod).String()
	if c.config.EnableNP {
		c.namedPort.AddNamedPortByPod(newPod)
		newNp := c.podMatchNetworkPolicies(newPod)
		if !maps.Equal(oldPod.Labels, newPod.Labels) {
			oldNp := c.podMatchNetworkPolicies(oldPod)
			for _, np := range util.DiffStringSlice(oldNp, newNp) {
				c.updateNpQueue.Add(np)
			}
		}

		for _, podNet := range podNets {
			oldAllocated := oldPod.Annotations[fmt.Sprintf(util.AllocatedAnnotationTemplate, podNet.ProviderName)]
			newAllocated := newPod.Annotations[fmt.Sprintf(util.AllocatedAnnotationTemplate, podNet.ProviderName)]
			if oldAllocated != newAllocated {
				for _, np := range newNp {
					klog.V(3).Infof("enqueue update network policy %s for pod %s", np, key)
					c.updateNpQueue.Add(np)
				}
				break
			}
		}
	}

	if c.config.EnableANP {
		nsLabels := c.getNsLabels(newPod.Namespace, newPod.Name)
		if !maps.Equal(oldPod.Labels, newPod.Labels) {
			c.updateAnpsByLabelsMatch(nsLabels, newPod.Labels)
			c.updateCnpsByLabelsMatch(nsLabels, newPod.Labels)
		}

		for _, podNet := range podNets {
			oldAllocated := oldPod.Annotations[fmt.Sprintf(util.AllocatedAnnotationTemplate, podNet.ProviderName)]
			newAllocated := newPod.Annotations[fmt.Sprintf(util.AllocatedAnnotationTemplate, podNet.ProviderName)]
			if oldAllocated != newAllocated {
				c.updateAnpsByLabelsMatch(nsLabels, newPod.Labels)
				c.updateCnpsByLabelsMatch(nsLabels, newPod.Labels)
				break
			}
		}
	}

	isStateful, statefulSetName, statefulSetUID := isStatefulSetPod(newPod)
	isVMPod, vmName := isVMPod(newPod)
	if !isPodStatusPhaseAlive(newPod) && !isStateful && !isVMPod {
		klog.V(3).Infof("enqueue delete pod %s", key)
		c.deletingPodObjMap.Store(key, newPod)
		c.deletePodQueue.Add(key)
		return
	}

	var delay time.Duration
	if newPod.Spec.TerminationGracePeriodSeconds != nil {
		if !newPod.DeletionTimestamp.IsZero() {
			delay = time.Until(newPod.DeletionTimestamp.Add(time.Duration(*newPod.Spec.TerminationGracePeriodSeconds) * time.Second))
		} else {
			delay = time.Duration(*newPod.Spec.TerminationGracePeriodSeconds) * time.Second
		}
	}

	if !newPod.DeletionTimestamp.IsZero() && !isStateful && !isVMPod {
		go func() {
			klog.V(3).Infof("enqueue delete pod %s after %v", key, delay)
			c.deletingPodObjMap.Store(key, newPod)
			c.deletePodQueue.AddAfter(key, delay)
		}()
		return
	}

	shouldDelete := (isStateful && isStatefulSetPodToDel(c.config.KubeClient, newPod, statefulSetName, statefulSetUID)) ||
		(isVMPod && c.isVMToDel(newPod, vmName))
	if shouldDelete {
		go func() {
			klog.V(3).Infof("enqueue delete pod %s after %v", key, delay)
			c.deletingPodObjMap.Store(key, newPod)
			c.deletePodQueue.AddAfter(key, delay)
		}()
		return
	}
	klog.Infof("enqueue update pod %s", key)
	c.addOrUpdatePodQueue.Add(key)

	for _, podNet := range podNets {
		oldSecurity := oldPod.Annotations[fmt.Sprintf(util.PortSecurityAnnotationTemplate, podNet.ProviderName)]
		newSecurity := newPod.Annotations[fmt.Sprintf(util.PortSecurityAnnotationTemplate, podNet.ProviderName)]
		oldSg := oldPod.Annotations[fmt.Sprintf(util.SecurityGroupAnnotationTemplate, podNet.ProviderName)]
		newSg := newPod.Annotations[fmt.Sprintf(util.SecurityGroupAnnotationTemplate, podNet.ProviderName)]
		oldVips := oldPod.Annotations[fmt.Sprintf(util.PortVipAnnotationTemplate, podNet.ProviderName)]
		newVips := newPod.Annotations[fmt.Sprintf(util.PortVipAnnotationTemplate, podNet.ProviderName)]
		oldAAPs := oldPod.Annotations[util.AAPsAnnotation]
		newAAPs := newPod.Annotations[util.AAPsAnnotation]
		if oldSecurity != newSecurity || oldSg != newSg || oldVips != newVips || oldAAPs != newAAPs {
			c.updatePodSecurityQueue.Add(key)
			break
		}
	}
}

func (c *Controller) getPodFabricNets(pod *v1.Pod) ([]*fabricNet, error) {
	attachmentNets, err := c.getPodAttachmentNet(pod)
	if err != nil {
		klog.Error(err)
		return nil, err
	}

	podNets := attachmentNets

	if c.config.EnableNonPrimaryCNI {
		return podNets, nil
	}

	defaultSubnet, err := c.getPodDefaultSubnet(pod)
	if err != nil {
		klog.Error(err)
		return nil, err
	}

	if defaultSubnet == nil {
		klog.Errorf("pod %s/%s has no default subnet, skip adding default network", pod.Namespace, pod.Name)
		return attachmentNets, nil
	}

	if _, hasOtherDefaultNet := pod.Annotations[util.DefaultNetworkAnnotation]; !hasOtherDefaultNet {
		podNets = append(attachmentNets, &fabricNet{
			Type:         providerTypeOriginal,
			ProviderName: util.OvnProvider,
			Subnet:       defaultSubnet,
			IsDefault:    true,
		})
	}

	return podNets, nil
}

func (c *Controller) handleAddOrUpdatePod(key string) (err error) {
	now := time.Now()
	klog.Infof("handle add/update pod %s", key)

	namespace, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("invalid resource key: %s", key))
		return nil
	}

	c.podKeyMutex.LockKey(key)
	defer func() {
		_ = c.podKeyMutex.UnlockKey(key)
		last := time.Since(now)
		klog.Infof("take %d ms to handle add or update pod %s", last.Milliseconds(), key)
	}()

	pod, err := c.podsLister.Pods(namespace).Get(name)
	if err != nil {
		if k8serrors.IsNotFound(err) {
			return nil
		}
		klog.Error(err)
		return err
	}
	if err := util.ValidatePodNetwork(pod.Annotations); err != nil {
		klog.Errorf("validate pod %s/%s failed: %v", namespace, name, err)
		c.recorder.Eventf(pod, v1.EventTypeWarning, "ValidatePodNetworkFailed", "%s", err.Error())
		return err
	}

	podNets, err := c.getPodFabricNets(pod)
	if err != nil {
		klog.Errorf("failed to get pod nets %v", err)
		c.recorder.Eventf(pod, v1.EventTypeWarning, "PodNetworkUpdateFailed", "stage=getPodFabricNets error=%v", err)
		return err
	}

	updatedPod, hotplugDetails, err := c.syncFabricNet(pod, podNets)
	if err != nil {
		klog.Errorf("failed to sync pod nets %v", err)
		c.recorder.Eventf(pod, v1.EventTypeWarning, "PodNetworkUpdateFailed", "stage=syncFabricNet error=%v", err)
		return err
	}
	pod = updatedPod
	if pod == nil {
		return nil
	}
	needAllocatePodNets := needAllocateSubnets(pod, podNets)
	if len(needAllocatePodNets) != 0 {
		if pod, err = c.reconcileAllocateSubnets(pod, needAllocatePodNets); err != nil {
			klog.Error(err)
			return err
		}
		if pod == nil {
			return nil
		}
	}

	if err = c.reconcilePodDHCPOptions(pod, podNets); err != nil {
		c.recorder.Eventf(pod, v1.EventTypeWarning, "PodNetworkUpdateFailed", "stage=reconcilePodDHCPOptions error=%v", err)
		return err
	}

	needRoutePodNets := needRouteSubnets(pod, podNets)
	if err = c.reconcileRouteSubnets(pod, needRoutePodNets); err != nil {
		c.recorder.Eventf(pod, v1.EventTypeWarning, "PodNetworkUpdateFailed", "stage=reconcileRouteSubnets error=%v", err)
		return err
	}

	if len(needAllocatePodNets) != 0 {
		details := c.podNetworkEventDetails(pod, needAllocatePodNets)
		if hotplugDetails != "" {
			details += "; " + hotplugDetails
		}
		c.recorder.Eventf(pod, v1.EventTypeNormal, "PodNetworkAllocated", "%s", details)
	} else if hotplugDetails != "" || len(needRoutePodNets) != 0 {
		details := []string{hotplugDetails}
		if routeDetails := c.podNetworkEventDetails(pod, needRoutePodNets); routeDetails != "" {
			details = append(details, routeDetails)
		}
		c.recorder.Eventf(pod, v1.EventTypeNormal, "PodNetworkUpdated", "%s", strings.TrimPrefix(strings.Join(details, "; "), "; "))
	}
	return nil
}

func subnetDHCPOptionsUUIDs(subnet *fabricv1.Subnet) *ovs.DHCPOptionsUUIDs {
	return &ovs.DHCPOptionsUUIDs{
		DHCPv4OptionsUUID: subnet.Status.DHCPv4OptionsUUID,
		DHCPv6OptionsUUID: subnet.Status.DHCPv6OptionsUUID,
	}
}

func dhcpOptionsForPodIPFamily(subnetDHCP *ovs.DHCPOptionsUUIDs, podIP, dhcpV4, dhcpV6 string) (*ovs.DHCPOptionsUUIDs, string, string) {
	filtered := &ovs.DHCPOptionsUUIDs{}
	if subnetDHCP != nil {
		*filtered = *subnetDHCP
	}
	switch util.CheckProtocol(podIP) {
	case fabricv1.ProtocolIPv4:
		filtered.DHCPv6OptionsUUID = ""
		return filtered, dhcpV4, ""
	case fabricv1.ProtocolIPv6:
		filtered.DHCPv4OptionsUUID = ""
		return filtered, "", dhcpV6
	}
	return filtered, dhcpV4, dhcpV6
}

func (c *Controller) reconcilePodDHCPOptions(pod *v1.Pod, podNets []*fabricNet) error {
	podName := c.getNameByPod(pod)
	for _, podNet := range podNets {
		if podNet.Type == providerTypeIPAM {
			continue
		}

		if pod.Annotations[fmt.Sprintf(util.AllocatedAnnotationTemplate, podNet.ProviderName)] != "true" {
			continue
		}

		subnet := podNet.Subnet
		portName := ovs.PodNameToPortName(podName, pod.Namespace, podNet.ProviderName)
		podIP := pod.Annotations[fmt.Sprintf(util.IPAddressAnnotationTemplate, podNet.ProviderName)]
		dhcpV4 := pod.Annotations[fmt.Sprintf(util.DHCPv4OptionsAnnotationTemplate, podNet.ProviderName)]
		dhcpV6 := pod.Annotations[fmt.Sprintf(util.DHCPv6OptionsAnnotationTemplate, podNet.ProviderName)]
		dhcpOptions, dhcpV4, dhcpV6 := dhcpOptionsForPodIPFamily(subnetDHCPOptionsUUIDs(subnet), podIP, dhcpV4, dhcpV6)

		var mtu int
		var gateway string
		if dhcpV4 != "" || dhcpV6 != "" {
			var err error
			if mtu, err = c.getSubnetMTU(subnet); err != nil {
				return err
			}
			gateway = subnet.Spec.Gateway
			if subnet.Status.U2OInterconnectionIP != "" && subnet.Spec.U2OInterconnection {
				gateway = subnet.Status.U2OInterconnectionIP
			}
		}

		if _, _, err := c.OVNNbClient.ReconcilePortDHCPOptions(
			subnet.Name, portName, dhcpOptions,
			subnet.Spec.CIDRBlock, gateway, dhcpV4, dhcpV6, mtu,
		); err != nil {
			klog.Errorf("failed to reconcile DHCP options for port %s: %v", portName, err)
			return err
		}
	}
	return nil
}

func (c *Controller) reconcileAllocateSubnets(pod *v1.Pod, needAllocatePodNets []*fabricNet) (*v1.Pod, error) {
	namespace := pod.Namespace
	name := pod.Name
	klog.Infof("sync pod %s/%s allocated", namespace, name)

	isVMPod, vmName := isVMPod(pod)
	podType := getPodType(pod)
	podName := c.getNameByPod(pod)

	eventPod := pod
	var err error
	recordFailure := func(stage string, err error) {
		c.recorder.Eventf(eventPod, v1.EventTypeWarning, "PodNetworkAllocationFailed", "stage=%s error=%v", stage, err)
	}
	var vmKey string
	if isVMPod && c.config.EnableKeepVMIP {
		vmKey = fmt.Sprintf("%s/%s", namespace, vmName)
	}

	patch := util.KVPatch{}
	for _, podNet := range needAllocatePodNets {
		v4IP, v6IP, mac, subnet, err := c.acquireAddress(pod, podNet)
		if err != nil {
			c.recorder.Eventf(pod, v1.EventTypeWarning, "AcquireAddressFailed", "stage=acquireAddress error=%v", err)
			klog.Error(err)
			return nil, err
		}
		podNet.Subnet = subnet
		ipStr := util.GetStringIP(v4IP, v6IP)
		patch[fmt.Sprintf(util.IPAddressAnnotationTemplate, podNet.ProviderName)] = ipStr
		if mac == "" {
			patch[fmt.Sprintf(util.MacAddressAnnotationTemplate, podNet.ProviderName)] = nil
		} else {
			patch[fmt.Sprintf(util.MacAddressAnnotationTemplate, podNet.ProviderName)] = mac
		}
		patch[fmt.Sprintf(util.CidrAnnotationTemplate, podNet.ProviderName)] = subnet.Spec.CIDRBlock
		patch[fmt.Sprintf(util.GatewayAnnotationTemplate, podNet.ProviderName)] = subnet.Spec.Gateway
		if isOvnSubnet(podNet.Subnet) {
			patch[fmt.Sprintf(util.LogicalSwitchAnnotationTemplate, podNet.ProviderName)] = subnet.Name
			if pod.Annotations[fmt.Sprintf(util.PodNicAnnotationTemplate, podNet.ProviderName)] == "" {
				patch[fmt.Sprintf(util.PodNicAnnotationTemplate, podNet.ProviderName)] = c.config.PodNicType
			}
		} else {
			patch[fmt.Sprintf(util.LogicalSwitchAnnotationTemplate, podNet.ProviderName)] = nil
			patch[fmt.Sprintf(util.PodNicAnnotationTemplate, podNet.ProviderName)] = nil
		}
		patch[fmt.Sprintf(util.AllocatedAnnotationTemplate, podNet.ProviderName)] = "true"
		if vmKey != "" {
			patch[fmt.Sprintf(util.VMAnnotationTemplate, podNet.ProviderName)] = vmName
		}
		if err := util.ValidateNetworkBroadcast(podNet.Subnet.Spec.CIDRBlock, ipStr); err != nil {
			klog.Errorf("validate pod %s/%s failed: %v", namespace, name, err)
			c.recorder.Eventf(pod, v1.EventTypeWarning, "ValidatePodNetworkFailed", "stage=validateNetworkBroadcast error=%v", err)
			return nil, err
		}

		if podNet.Type != providerTypeIPAM {
			if (subnet.Spec.Vlan == "" || subnet.Spec.LogicalGateway || subnet.Spec.U2OInterconnection) && subnet.Spec.Vpc != "" {
				patch[fmt.Sprintf(util.LogicalRouterAnnotationTemplate, podNet.ProviderName)] = subnet.Spec.Vpc
			}

			if subnet.Spec.Vlan != "" {
				vlan, err := c.vlansLister.Get(subnet.Spec.Vlan)
				if err != nil {
					klog.Error(err)
					c.recorder.Eventf(pod, v1.EventTypeWarning, "GetVlanInfoFailed", "stage=getVlanInfo error=%v", err)
					return nil, err
				}
				patch[fmt.Sprintf(util.VlanIDAnnotationTemplate, podNet.ProviderName)] = strconv.Itoa(vlan.Spec.ID)
				patch[fmt.Sprintf(util.ProviderNetworkTemplate, podNet.ProviderName)] = vlan.Spec.Provider
			}

			portSecurity := false
			if pod.Annotations[fmt.Sprintf(util.PortSecurityAnnotationTemplate, podNet.ProviderName)] == "true" {
				portSecurity = true
			}

			vips := c.getVirtualIPs(pod, []*fabricNet{podNet})[fmt.Sprintf("%s.%s", podNet.Subnet.Name, podNet.ProviderName)]
			for ip := range strings.SplitSeq(vips, ",") {
				if ip != "" && net.ParseIP(ip) == nil {
					klog.Errorf("invalid vip address '%s' for pod %s", ip, name)
					vips = ""
					break
				}
			}

			portName := ovs.PodNameToPortName(podName, namespace, podNet.ProviderName)

			dhcpV4 := pod.Annotations[fmt.Sprintf(util.DHCPv4OptionsAnnotationTemplate, podNet.ProviderName)]
			dhcpV6 := pod.Annotations[fmt.Sprintf(util.DHCPv6OptionsAnnotationTemplate, podNet.ProviderName)]
			subnetDHCP, dhcpV4, dhcpV6 := dhcpOptionsForPodIPFamily(subnetDHCPOptionsUUIDs(subnet), ipStr, dhcpV4, dhcpV6)

			var mtu int
			var gateway string
			if dhcpV4 != "" || dhcpV6 != "" {
				if mtu, err = c.getSubnetMTU(subnet); err != nil {
					recordFailure("getSubnetMTU", err)
					return nil, err
				}
				gateway = subnet.Spec.Gateway
				if subnet.Status.U2OInterconnectionIP != "" && subnet.Spec.U2OInterconnection {
					gateway = subnet.Status.U2OInterconnectionIP
				}
			}

			dhcpOptions, hasPerPortDHCP, err := c.OVNNbClient.ReconcilePortDHCPOptions(
				subnet.Name, portName, subnetDHCP,
				subnet.Spec.CIDRBlock, gateway, dhcpV4, dhcpV6, mtu,
			)
			if err != nil {
				klog.Errorf("failed to reconcile DHCP options for port %s: %v", portName, err)
				recordFailure("reconcilePortDHCPOptions", err)
				return nil, err
			}

			enableDHCP := podNet.Subnet.Spec.EnableDHCP || hasPerPortDHCP

			var oldSgList []string
			if vmKey != "" {
				existingLsp, err := c.OVNNbClient.GetLogicalSwitchPort(portName, true)
				if err != nil {
					klog.Errorf("failed to get logical switch port %s: %v", portName, err)
					recordFailure("getLogicalSwitchPort", err)
					return nil, err
				}
				if existingLsp != nil {
					oldSgList, _ = c.getPortSg(existingLsp)
				}
			}

			securityGroupAnnotation := pod.Annotations[fmt.Sprintf(util.SecurityGroupAnnotationTemplate, podNet.ProviderName)]
			if err := c.OVNNbClient.CreateLogicalSwitchPort(subnet.Name, portName, ipStr, mac, podName, pod.Namespace,
				portSecurity, securityGroupAnnotation, vips, enableDHCP, dhcpOptions, subnet.Spec.Vpc); err != nil {
				c.recorder.Eventf(pod, v1.EventTypeWarning, "CreateOVNPortFailed", "stage=createLogicalSwitchPort error=%v", err)
				klog.Errorf("%v", err)
				return nil, err
			}

			if pod.Annotations[fmt.Sprintf(util.Layer2ForwardAnnotationTemplate, podNet.ProviderName)] == "true" {
				if err := c.OVNNbClient.EnablePortLayer2forward(portName); err != nil {
					c.recorder.Eventf(pod, v1.EventTypeWarning, "SetOVNPortL2ForwardFailed", "stage=setLogicalSwitchPortLayer2Forward error=%v", err)
					klog.Errorf("%v", err)
					return nil, err
				}
			}

			if securityGroupAnnotation != "" || oldSgList != nil {
				securityGroups := strings.ReplaceAll(securityGroupAnnotation, " ", "")
				newSgList := strings.Split(securityGroups, ",")
				sgNames := util.UnionStringSlice(oldSgList, newSgList)
				for _, sgName := range sgNames {
					if sgName != "" {
						c.syncSgPortsQueue.Add(sgName)
					}
				}
			}

			if vips != "" {
				c.syncVirtualPortsQueue.Add(podNet.Subnet.Name)
			}
		}

		ipCRName := ovs.PodNameToPortName(podName, pod.Namespace, podNet.ProviderName)
		if err := c.createOrUpdateIPCR(ipCRName, podName, ipStr, mac, subnet.Name, pod.Namespace, pod.Spec.NodeName, podType); err != nil {
			err = fmt.Errorf("failed to create ips CR %s.%s: %w", podName, pod.Namespace, err)
			klog.Error(err)
			recordFailure("createOrUpdateIPCR", err)
			return nil, err
		}
	}
	if err = util.PatchAnnotations(c.config.KubeClient.CoreV1().Pods(namespace), name, patch); err != nil {
		if k8serrors.IsNotFound(err) {
			key := strings.Join([]string{namespace, name}, "/")
			c.deletingPodObjMap.Store(key, pod)
			c.deletePodQueue.AddRateLimited(key)
			return nil, nil
		}
		klog.Errorf("failed to patch pod %s/%s: %v", namespace, name, err)
		recordFailure("patchPodAnnotations", err)
		return nil, err
	}

	updatedPod, err := c.config.KubeClient.CoreV1().Pods(namespace).Get(context.TODO(), name, metav1.GetOptions{})
	if err != nil {
		if k8serrors.IsNotFound(err) {
			key := strings.Join([]string{namespace, name}, "/")
			c.deletingPodObjMap.Store(key, pod)
			c.deletePodQueue.AddRateLimited(key)
			return nil, nil
		}
		klog.Errorf("failed to get pod %s/%s: %v", namespace, name, err)
		recordFailure("getPatchedPod", err)
		return nil, err
	}
	pod = updatedPod

	if isVMPod && c.config.EnableKeepVMIP {
		c.cleanStaleVMAttachmentIPs(pod, podName)
	}

	return pod, nil
}

func (c *Controller) reconcileRouteSubnets(pod *v1.Pod, needRoutePodNets []*fabricNet) error {
	if len(needRoutePodNets) == 0 {
		return nil
	}

	namespace := pod.Namespace
	name := pod.Name
	podName := c.getNameByPod(pod)

	klog.Infof("sync pod %s/%s routed", namespace, name)

	node, err := c.nodesLister.Get(pod.Spec.NodeName)
	if err != nil {
		klog.Errorf("failed to get node %s: %v", pod.Spec.NodeName, err)
		return err
	}

	portGroups, err := c.OVNNbClient.ListPortGroups(map[string]string{"node": "", networkPolicyKey: ""})
	if err != nil {
		klog.Errorf("failed to list port groups: %v", err)
		return err
	}

	var nodePortGroups []string
	nodePortGroup := strings.ReplaceAll(node.Annotations[util.PortNameAnnotation], "-", ".")
	for _, pg := range portGroups {
		if pg.Name != nodePortGroup && pg.ExternalIDs["subnet"] == "" {
			nodePortGroups = append(nodePortGroups, pg.Name)
		}
	}

	var podIP string
	var subnet *fabricv1.Subnet
	patch := util.KVPatch{}
	for _, podNet := range needRoutePodNets {
		if pod.Annotations[fmt.Sprintf(util.AllocatedAnnotationTemplate, podNet.ProviderName)] == "" {
			return fmt.Errorf("no address has been allocated to %s/%s", namespace, name)
		}

		podIP = pod.Annotations[fmt.Sprintf(util.IPAddressAnnotationTemplate, podNet.ProviderName)]
		subnet = podNet.Subnet

		if subnet.Name == c.config.NodeSwitch {
			return fmt.Errorf("NodeSwitch subnet %s is unavailable for pod", subnet.Name)
		}

		if portGroups, err = c.OVNNbClient.ListPortGroups(map[string]string{"subnet": subnet.Name, "node": "", networkPolicyKey: ""}); err != nil {
			klog.Errorf("failed to list port groups: %v", err)
			return err
		}

		pgName := getOverlaySubnetsPortGroupName(subnet.Name, pod.Spec.NodeName)
		portName := ovs.PodNameToPortName(podName, pod.Namespace, podNet.ProviderName)
		subnetPortGroups := make([]string, 0, len(portGroups))
		for _, pg := range portGroups {
			if pg.Name != pgName {
				subnetPortGroups = append(subnetPortGroups, pg.Name)
			}
		}

		if (!c.config.EnableLb || (subnet.Spec.EnableLb == nil || !*subnet.Spec.EnableLb)) &&
			subnet.Spec.Vpc == c.config.ClusterRouter &&
			subnet.Spec.U2OInterconnection &&
			subnet.Spec.Vlan != "" &&
			!subnet.Spec.LogicalGateway {
			if err = c.OVNNbClient.RemovePortFromPortGroups(portName, subnetPortGroups...); err != nil {
				klog.Errorf("failed to remove port %s from port groups %v: %v", portName, subnetPortGroups, err)
				return err
			}

			if err := c.OVNNbClient.PortGroupAddPorts(pgName, portName); err != nil {
				klog.Errorf("failed to add port to u2o port group %s: %v", pgName, err)
				return err
			}
		}

		if podIP != "" && (subnet.Spec.Vlan == "" || subnet.Spec.LogicalGateway) && subnet.Spec.Vpc == c.config.ClusterRouter {
			if err = c.OVNNbClient.RemovePortFromPortGroups(portName, nodePortGroups...); err != nil {
				klog.Errorf("failed to remove port %s from port groups %v: %v", portName, nodePortGroups, err)
				return err
			}

			if err = c.OVNNbClient.PortGroupAddPorts(nodePortGroup, portName); err != nil {
				klog.Errorf("failed to add port %s to port group %s: %v", portName, nodePortGroup, err)
				return err
			}

			if subnet.Spec.GatewayType == fabricv1.GWDistributedType && pod.Annotations[util.NorthGatewayAnnotation] == "" {
				nodeTunlIPAddr, err := getNodeTunlIP(node)
				if err != nil {
					klog.Error(err)
					return err
				}

				var added bool
				for _, nodeAddr := range nodeTunlIPAddr {
					for podAddr := range strings.SplitSeq(podIP, ",") {
						if util.CheckProtocol(nodeAddr.String()) != util.CheckProtocol(podAddr) {
							continue
						}

						if err = c.OVNNbClient.RemovePortFromPortGroups(portName, subnetPortGroups...); err != nil {
							klog.Errorf("failed to remove port %s from port groups %v: %v", portName, subnetPortGroups, err)
							return err
						}
						if err := c.OVNNbClient.PortGroupAddPorts(pgName, portName); err != nil {
							klog.Errorf("failed to add port %s to port group %s: %v", portName, pgName, err)
							return err
						}

						added = true
						break
					}
					if added {
						break
					}
				}
			}

			if pod.Annotations[util.NorthGatewayAnnotation] != "" && pod.Annotations[util.IPAddressAnnotation] != "" {
				for podAddr := range strings.SplitSeq(pod.Annotations[util.IPAddressAnnotation], ",") {
					if util.CheckProtocol(podAddr) != util.CheckProtocol(pod.Annotations[util.NorthGatewayAnnotation]) {
						continue
					}
					ipSuffix := "ip4"
					if util.CheckProtocol(podAddr) == fabricv1.ProtocolIPv6 {
						ipSuffix = "ip6"
					}

					if err := c.addPolicyRouteToVpc(
						subnet.Spec.Vpc,
						&fabricv1.PolicyRoute{
							Priority:  util.NorthGatewayRoutePolicyPriority,
							Match:     fmt.Sprintf("%s.src == %s", ipSuffix, podAddr),
							Action:    fabricv1.PolicyRouteActionReroute,
							NextHopIP: pod.Annotations[util.NorthGatewayAnnotation],
						},
						map[string]string{
							"vendor": util.VendorTag,
							"subnet": subnet.Name,
						},
					); err != nil {
						klog.Errorf("failed to add policy route, %v", err)
						return err
					}
				}
			} else {
				if err = c.deleteStaticRouteFromVpc(
					c.config.ClusterRouter,
					subnet.Spec.RouteTable,
					podIP,
					"",
					fabricv1.PolicyDst,
				); err != nil {
					klog.Error(err)
					return err
				}
			}
		}

		if pod.Annotations[fmt.Sprintf(util.ActivationStrategyTemplate, podNet.ProviderName)] != "" {
			if err := c.OVNNbClient.SetLogicalSwitchPortActivationStrategy(portName, pod.Spec.NodeName); err != nil {
				klog.Errorf("failed to set activation strategy for lsp %s: %v", portName, err)
				return err
			}
		}

		patch[fmt.Sprintf(util.RoutedAnnotationTemplate, podNet.ProviderName)] = "true"
	}
	if err := util.PatchAnnotations(c.config.KubeClient.CoreV1().Pods(namespace), name, patch); err != nil {
		if k8serrors.IsNotFound(err) {
			key := strings.Join([]string{namespace, name}, "/")
			c.deletingPodObjMap.Store(key, pod)
			c.deletePodQueue.AddRateLimited(key)
			return nil
		}
		klog.Errorf("failed to patch pod %s/%s: %v", namespace, name, err)
		return err
	}
	return nil
}

func (c *Controller) handleDeletePod(key string) (err error) {
	pod, ok := c.deletingPodObjMap.Load(key)
	if !ok {
		return nil
	}
	now := time.Now()
	klog.Infof("handle delete pod %s", key)
	podName := c.getNameByPod(pod)
	changed := false
	stage := "prepare"
	released := []string{}
	var podNets []*fabricNet
	var keepIPCR, isOwnerRefToDel, isOwnerRefDeleted bool
	var ipcrToDelete []string
	var vmOrphanedPorts map[string]bool
	c.podKeyMutex.LockKey(key)
	defer func() {
		_ = c.podKeyMutex.UnlockKey(key)
		if err != nil {
			c.recorder.Eventf(pod, v1.EventTypeWarning, "PodNetworkReleaseFailed", "stage=%s error=%v", stage, err)
		} else if changed {
			details := strings.Join(released, "; ")
			if !keepIPCR {
				if networkDetails := c.podNetworkEventDetails(pod, podNets); networkDetails != "" {
					details += "; " + networkDetails
				}
			}
			c.recorder.Eventf(pod, v1.EventTypeNormal, "PodNetworkReleased", "%s", strings.TrimPrefix(details, "; "))
		}
		if err == nil {
			c.deletingPodObjMap.Delete(key)
		}
		last := time.Since(now)
		klog.Infof("take %d ms to handle delete pod %s", last.Milliseconds(), key)
	}()

	p, _ := c.podsLister.Pods(pod.Namespace).Get(pod.Name)
	if p != nil && p.UID != pod.UID {
		return nil
	}

	if aaps := pod.Annotations[util.AAPsAnnotation]; aaps != "" {
		for vipName := range strings.SplitSeq(aaps, ",") {
			if vip, err := c.virtualIpsLister.Get(vipName); err == nil {
				if vip.Spec.Namespace != pod.Namespace {
					continue
				}
				klog.Infof("enqueue update virtual parents for %s", vipName)
				c.updateVirtualParentsQueue.Add(vipName)
			}
		}
	}

	podKey := fmt.Sprintf("%s/%s", pod.Namespace, podName)

	isStsPod, stsName, stsUID := isStatefulSetPod(pod)
	if isStsPod {
		if !pod.DeletionTimestamp.IsZero() {
			klog.Infof("handle deletion of sts pod %s", podKey)
			isOwnerRefToDel = isStatefulSetPodToDel(c.config.KubeClient, pod, stsName, stsUID)
			if !isOwnerRefToDel {
				klog.Infof("try keep ip for sts pod %s", podKey)
				keepIPCR = true
			}
		}
		if keepIPCR {
			stage = "checkStatefulSetOwner"
			isOwnerRefDeleted, ipcrToDelete, err = appendCheckPodNetToDel(c, pod, stsName, util.KindStatefulSet)
			if err != nil {
				klog.Error(err)
				return err
			}
			if isOwnerRefDeleted || len(ipcrToDelete) != 0 {
				klog.Infof("not keep ip for sts pod %s", podKey)
				keepIPCR = false
			}
		}
	}
	stage = "listLogicalSwitchPorts"
	ports, err := c.OVNNbClient.ListNormalLogicalSwitchPorts(true, map[string]string{"pod": podKey})
	if err != nil {
		klog.Errorf("failed to list lsps of pod %s: %v", podKey, err)
		return err
	}
	stage = "releaseNetworkResources"

	var hasAliveVMSibling bool
	isVMPod, vmName := isVMPod(pod)
	if isVMPod && c.config.EnableKeepVMIP {
		for _, port := range ports {
			stage = "cleanLogicalSwitchPortMigrateOptions"
			if err := c.OVNNbClient.CleanLogicalSwitchPortMigrateOptions(port.Name); err != nil {
				err = fmt.Errorf("failed to clean migrate options for vm lsp %s, %w", port.Name, err)
				klog.Error(err)
				return err
			}
		}
		if pod.DeletionTimestamp != nil {
			klog.Infof("handle deletion of vm pod %s", podKey)
			isOwnerRefToDel = c.isVMToDel(pod, vmName)
			if !isOwnerRefToDel {
				klog.Infof("try keep ip for vm pod %s", podKey)
				keepIPCR = true

				stage = "listVMPodSiblings"
				siblings, listErr := c.podsLister.Pods(pod.Namespace).List(labels.Everything())
				if listErr != nil {
					klog.Errorf("failed to list pods in namespace %s: %v", pod.Namespace, listErr)
					return listErr
				}
				hasAliveVMSibling = hasAliveSiblingVMPod(siblings, vmName, pod.Name)
			}
		}
		if keepIPCR {
			stage = "checkVMOwner"
			isOwnerRefDeleted, ipcrToDelete, err = appendCheckPodNetToDel(c, pod, vmName, util.KindVirtualMachineInstance)
			if err != nil {
				klog.Error(err)
				return err
			}
			if isOwnerRefDeleted || len(ipcrToDelete) != 0 {
				klog.Infof("not keep ip for vm pod %s", podKey)
				keepIPCR = false
			}
		}

		if keepIPCR {
			vmOrphanedPorts = c.getVMOrphanedAttachmentPorts(pod.Namespace, vmName, ports)
		}
	}

	stage = "getPodFabricNets"
	podNets, err = c.getPodFabricNets(pod)
	if err != nil {
		klog.Errorf("failed to get fabric nets of pod %s: %v", podKey, err)
		return err
	}
	if keepIPCR {
		for _, port := range ports {
			switch {
			case vmOrphanedPorts[port.Name]:

				stage = "getIPCR"
				ipCR, getErr := c.ipsLister.Get(port.Name)
				if getErr != nil && !k8serrors.IsNotFound(getErr) {
					klog.Errorf("failed to get ip %s: %v", port.Name, getErr)
					return getErr
				}
				klog.Infof("delete orphaned vm attachment lsp %s", port.Name)
				stage = "deleteLogicalSwitchPort"
				if err := c.OVNNbClient.DeleteLogicalSwitchPort(port.Name); err != nil {
					klog.Errorf("failed to delete orphaned lsp %s: %v", port.Name, err)
					return err
				}
				changed = true
				released = append(released, "logicalSwitchPort="+port.Name)
				if k8serrors.IsNotFound(getErr) {
					continue
				}
				if ipCR.Labels[util.IPReservedLabel] != "true" {
					klog.Infof("delete orphaned vm attachment ip CR %s", ipCR.Name)
					stage = "deleteIPCR"
					if err := c.config.FabricClient.FabricV1().IPs().Delete(context.Background(), ipCR.Name, metav1.DeleteOptions{}); err != nil {
						if !k8serrors.IsNotFound(err) {
							klog.Errorf("failed to delete ip %s: %v", ipCR.Name, err)
							return err
						}
					} else {
						changed = true
						released = append(released, fmt.Sprintf("ipCR=%s subnet=%s", ipCR.Name, ipCR.Spec.Subnet))
					}
					if subnetName := ipCR.Spec.Subnet; subnetName != "" {
						addressCount := len(c.ipam.GetPodAddress(podKey))
						c.ipam.ReleaseAddressByNic(podKey, port.Name, subnetName)
						if len(c.ipam.GetPodAddress(podKey)) < addressCount {
							changed = true
							released = append(released, fmt.Sprintf("ipam=%s subnet=%s", port.Name, subnetName))
						}
						c.updateSubnetStatusQueue.Add(subnetName)
					}
				}
			case hasAliveVMSibling:
				klog.Infof("skip removing lsp %s from port groups: another alive virt-launcher pod exists for vm %s/%s", port.Name, pod.Namespace, vmName)
			default:
				klog.Infof("remove lsp %s from all port groups", port.Name)
				stage = "removeLogicalSwitchPortFromPortGroups"
				if err = c.OVNNbClient.RemovePortFromPortGroups(port.Name); err != nil {
					klog.Errorf("failed to remove lsp %s from all port groups: %v", port.Name, err)
					return err
				}
			}
		}
	} else {
		if len(ports) != 0 {
			addresses := c.ipam.GetPodAddress(podKey)
			for _, address := range addresses {
				if strings.TrimSpace(address.IP) == "" {
					continue
				}
				subnet, err := c.subnetsLister.Get(address.Subnet.Name)
				if k8serrors.IsNotFound(err) {
					continue
				} else if err != nil {
					klog.Error(err)
					return err
				}
				vpc, err := c.vpcsLister.Get(subnet.Spec.Vpc)
				if k8serrors.IsNotFound(err) {
					continue
				} else if err != nil {
					klog.Error(err)
					return err
				}

				ipSuffix := "ip4"
				if util.CheckProtocol(address.IP) == fabricv1.ProtocolIPv6 {
					ipSuffix = "ip6"
				}
				if err = c.deletePolicyRouteFromVpc(
					vpc.Name,
					util.NorthGatewayRoutePolicyPriority,
					fmt.Sprintf("%s.src == %s", ipSuffix, address.IP),
				); err != nil {
					klog.Errorf("failed to delete static route, %v", err)
					return err
				}
			}
		}
		for _, port := range ports {
			klog.Infof("delete logical switch port %s", port.Name)
			stage = "deleteLogicalSwitchPort"
			if err := c.OVNNbClient.DeleteLogicalSwitchPort(port.Name); err != nil {
				klog.Errorf("failed to delete lsp %s, %v", port.Name, err)
				return err
			}
			changed = true
			released = append(released, "logicalSwitchPort="+port.Name)
		}
		klog.Infof("try release all ip address for deleting pod %s", podKey)
		for _, podNet := range podNets {
			portName := ovs.PodNameToPortName(podName, pod.Namespace, podNet.ProviderName)

			if (isStsPod || isVMPod) && !isOwnerRefToDel && !isOwnerRefDeleted &&
				!slices.Contains(ipcrToDelete, portName) {
				klog.Infof("skip clean ip CR %s", portName)
				continue
			}
			ipCR, err := c.ipsLister.Get(portName)
			if err != nil {
				if k8serrors.IsNotFound(err) {
					continue
				}
				klog.Errorf("failed to get ip %s, %v", portName, err)
				return err
			}
			if ipCR.Labels[util.IPReservedLabel] != "true" {
				klog.Infof("delete ip CR %s", ipCR.Name)
				stage = "deleteIPCR"
				if err := c.config.FabricClient.FabricV1().IPs().Delete(context.Background(), ipCR.Name, metav1.DeleteOptions{}); err != nil {
					if !k8serrors.IsNotFound(err) {
						klog.Errorf("failed to delete ip %s, %v", ipCR.Name, err)
						return err
					}
				} else {
					changed = true
					released = append(released, fmt.Sprintf("ipCR=%s subnet=%s", ipCR.Name, podNet.Subnet.Name))
				}

				addressCount := len(c.ipam.GetPodAddress(podKey))
				c.ipam.ReleaseAddressByNic(podKey, portName, podNet.Subnet.Name)
				if len(c.ipam.GetPodAddress(podKey)) < addressCount {
					changed = true
					released = append(released, fmt.Sprintf("ipam=%s subnet=%s", portName, podNet.Subnet.Name))
				}

				c.updateSubnetStatusQueue.Add(podNet.Subnet.Name)
			}
		}
		if pod.Annotations[util.VipAnnotation] != "" {
			vip, vipErr := c.virtualIpsLister.Get(pod.Annotations[util.VipAnnotation])
			vipWillChange := vipErr == nil && vip.Labels[util.IPReservedLabel] != ""
			stage = "releaseVIP"
			if err = c.releaseVip(pod.Annotations[util.VipAnnotation]); err != nil {
				klog.Errorf("failed to clean label from vip %s, %v", pod.Annotations[util.VipAnnotation], err)
				return err
			}
			if vipWillChange {
				changed = true
				released = append(released, "vip="+pod.Annotations[util.VipAnnotation])
			}
		}
	}
	for _, podNet := range podNets {
		if !isOvnSubnet(podNet.Subnet) {
			continue
		}

		c.syncVirtualPortsQueue.Add(podNet.Subnet.Name)
		securityGroupAnnotation := pod.Annotations[fmt.Sprintf(util.SecurityGroupAnnotationTemplate, podNet.ProviderName)]
		if securityGroupAnnotation != "" {
			securityGroups := strings.ReplaceAll(securityGroupAnnotation, " ", "")
			for sgName := range strings.SplitSeq(securityGroups, ",") {
				if sgName != "" {
					c.syncSgPortsQueue.Add(sgName)
				}
			}
		}
	}
	return nil
}

func (c *Controller) handleUpdatePodSecurity(key string) error {
	now := time.Now()
	klog.Infof("handle add/update pod security group %s", key)

	namespace, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("invalid resource key: %s", key))
		return nil
	}

	c.podKeyMutex.LockKey(key)
	defer func() {
		_ = c.podKeyMutex.UnlockKey(key)
		last := time.Since(now)
		klog.Infof("take %d ms to handle sg for pod %s", last.Milliseconds(), key)
	}()

	pod, err := c.podsLister.Pods(namespace).Get(name)
	if err != nil {
		if k8serrors.IsNotFound(err) {
			return nil
		}
		klog.Error(err)
		return err
	}
	podName := c.getNameByPod(pod)

	podNets, err := c.getPodFabricNets(pod)
	if err != nil {
		klog.Errorf("failed to pod nets %v", err)
		c.recorder.Eventf(pod, v1.EventTypeWarning, "PodSecurityUpdateFailed", "stage=getPodFabricNets error=%v", err)
		return err
	}

	vipsMap := c.getVirtualIPs(pod, podNets)
	updatedPodNets := make([]*fabricNet, 0, len(podNets))

	for _, podNet := range podNets {
		if !isOvnSubnet(podNet.Subnet) {
			continue
		}

		portSecurity := false
		if pod.Annotations[fmt.Sprintf(util.PortSecurityAnnotationTemplate, podNet.ProviderName)] == "true" {
			portSecurity = true
		}

		mac := pod.Annotations[fmt.Sprintf(util.MacAddressAnnotationTemplate, podNet.ProviderName)]
		ipStr := pod.Annotations[fmt.Sprintf(util.IPAddressAnnotationTemplate, podNet.ProviderName)]
		vips := vipsMap[fmt.Sprintf("%s.%s", podNet.Subnet.Name, podNet.ProviderName)]
		portName := ovs.PodNameToPortName(podName, namespace, podNet.ProviderName)
		if err = c.OVNNbClient.SetLogicalSwitchPortSecurity(portSecurity, portName, mac, ipStr, vips); err != nil {
			klog.Errorf("failed to set security for logical switch port %s: %v", portName, err)
			c.recorder.Eventf(pod, v1.EventTypeWarning, "PodSecurityUpdateFailed", "stage=setLogicalSwitchPortSecurity error=%v", err)
			return err
		}

		c.syncVirtualPortsQueue.Add(podNet.Subnet.Name)
		securityGroupAnnotation := pod.Annotations[fmt.Sprintf(util.SecurityGroupAnnotationTemplate, podNet.ProviderName)]
		var securityGroups string
		if securityGroupAnnotation != "" {
			securityGroups = strings.ReplaceAll(securityGroupAnnotation, " ", "")
			for sgName := range strings.SplitSeq(securityGroups, ",") {
				if sgName != "" {
					c.syncSgPortsQueue.Add(sgName)
				}
			}
		}
		if err = c.reconcilePortSg(portName, securityGroups); err != nil {
			klog.Errorf("reconcilePortSg failed. %v", err)
			c.recorder.Eventf(pod, v1.EventTypeWarning, "PodSecurityUpdateFailed", "stage=reconcilePortSg error=%v", err)
			return err
		}
		updatedPodNets = append(updatedPodNets, podNet)
	}
	if len(updatedPodNets) != 0 {
		c.recorder.Eventf(pod, v1.EventTypeNormal, "PodSecurityUpdated", "%s", c.podNetworkEventDetails(pod, updatedPodNets))
	}
	return nil
}

func stalePortNetworkDetails(pod *v1.Pod, podName string, port ovnnb.LogicalSwitchPort) (string, string, error) {
	portPrefix := ovs.PodNameToPortName(podName, pod.Namespace, util.OvnProvider)
	providerName := util.OvnProvider
	if port.Name != portPrefix {
		var ok bool
		providerName, ok = strings.CutPrefix(port.Name, portPrefix+".")
		if !ok || providerName == "" {
			return "", "", fmt.Errorf("logical switch port %q does not match pod prefix %q", port.Name, portPrefix)
		}
	}
	details := fmt.Sprintf("provider=%s subnet=%s ip=%s mac=%s logicalSwitchPort=%s",
		providerName,
		port.ExternalIDs["ls"],
		pod.Annotations[fmt.Sprintf(util.IPAddressAnnotationTemplate, providerName)],
		pod.Annotations[fmt.Sprintf(util.MacAddressAnnotationTemplate, providerName)],
		port.Name,
	)
	return providerName, details, nil
}

func (c *Controller) syncFabricNet(pod *v1.Pod, podNets []*fabricNet) (*v1.Pod, string, error) {
	podName := c.getNameByPod(pod)
	key := cache.NewObjectName(pod.Namespace, podName).String()
	targetPortNameList := strset.NewWithSize(len(podNets))
	portsNeedToDel := []string{}
	annotationsNeedToDel := []string{}
	annotationsNeedToAdd := make(map[string]string)
	subnetUsedByPort := make(map[string]string)
	changedPodNets := []*fabricNet{}
	hotplugDetails := []string{}

	for _, podNet := range podNets {
		portName := ovs.PodNameToPortName(podName, pod.Namespace, podNet.ProviderName)
		targetPortNameList.Add(portName)
		changed := false
		if podNet.IPRequest != "" && pod.Annotations[fmt.Sprintf(util.IPAddressAnnotationTemplate, podNet.ProviderName)] != podNet.IPRequest {
			klog.Infof("pod %s/%s use custom IP %s for provider %s", pod.Namespace, pod.Name, podNet.IPRequest, podNet.ProviderName)
			annotationsNeedToAdd[fmt.Sprintf(util.IPAddressAnnotationTemplate, podNet.ProviderName)] = podNet.IPRequest
			changed = true
		}

		if podNet.MacRequest != "" && pod.Annotations[fmt.Sprintf(util.MacAddressAnnotationTemplate, podNet.ProviderName)] != podNet.MacRequest {
			klog.Infof("pod %s/%s use custom MAC %s for provider %s", pod.Namespace, pod.Name, podNet.MacRequest, podNet.ProviderName)
			annotationsNeedToAdd[fmt.Sprintf(util.MacAddressAnnotationTemplate, podNet.ProviderName)] = podNet.MacRequest
			changed = true
		}
		if changed {
			changedPodNets = append(changedPodNets, podNet)
		}
	}

	ports, err := c.OVNNbClient.ListNormalLogicalSwitchPorts(true, map[string]string{"pod": key})
	if err != nil {
		klog.Errorf("failed to list lsps of pod '%s', %v", pod.Name, err)
		return nil, "", err
	}

	for _, port := range ports {
		if !targetPortNameList.Has(port.Name) {
			portsNeedToDel = append(portsNeedToDel, port.Name)
			subnetUsedByPort[port.Name] = port.ExternalIDs["ls"]
			providerName, details, parseErr := stalePortNetworkDetails(pod, podName, port)
			if parseErr != nil {
				klog.Warning(parseErr)
				hotplugDetails = append(hotplugDetails, fmt.Sprintf("provider=unknown subnet=%s ip= mac= logicalSwitchPort=%s", port.ExternalIDs["ls"], port.Name))
				continue
			}
			hotplugDetails = append(hotplugDetails, details)
			if providerName == util.OvnProvider {
				continue
			}
			annotationsNeedToDel = append(annotationsNeedToDel, providerName)
		}
	}

	if len(portsNeedToDel) == 0 && len(annotationsNeedToAdd) == 0 {
		return pod, "", nil
	}

	for _, portNeedDel := range portsNeedToDel {
		klog.Infof("release port %s for pod %s", portNeedDel, podName)
		c.ipam.ReleaseAddressByNic(key, portNeedDel, subnetUsedByPort[portNeedDel])
		if err := c.OVNNbClient.DeleteLogicalSwitchPort(portNeedDel); err != nil {
			klog.Errorf("failed to delete lsp %s, %v", portNeedDel, err)
			return nil, "", err
		}
		if err := c.config.FabricClient.FabricV1().IPs().Delete(context.Background(), portNeedDel, metav1.DeleteOptions{}); err != nil {
			if !k8serrors.IsNotFound(err) {
				klog.Errorf("failed to delete ip %s, %v", portNeedDel, err)
				return nil, "", err
			}
		}
	}

	patch := util.KVPatch{}
	for _, providerName := range annotationsNeedToDel {
		for key := range pod.Annotations {
			if strings.HasPrefix(key, providerName) {
				patch[key] = nil
			}
		}
	}

	for key, value := range annotationsNeedToAdd {
		patch[key] = value
	}

	if len(patch) == 0 {
		return pod, strings.Join(hotplugDetails, "; "), nil
	}

	if err = util.PatchAnnotations(c.config.KubeClient.CoreV1().Pods(pod.Namespace), pod.Name, patch); err != nil {
		if k8serrors.IsNotFound(err) {
			return nil, "", nil
		}
		klog.Errorf("failed to clean annotations for pod %s/%s: %v", pod.Namespace, pod.Name, err)
		return nil, "", err
	}

	if pod, err = c.config.KubeClient.CoreV1().Pods(pod.Namespace).Get(context.TODO(), pod.Name, metav1.GetOptions{}); err != nil {
		if k8serrors.IsNotFound(err) {
			return nil, "", nil
		}
		klog.Errorf("failed to get pod %s/%s: %v", pod.Namespace, pod.Name, err)
		return nil, "", err
	}

	if details := c.podNetworkEventDetails(pod, changedPodNets); details != "" {
		hotplugDetails = append(hotplugDetails, details)
	}
	return pod, strings.Join(hotplugDetails, "; "), nil
}

func (c *Controller) podNetworkEventDetails(pod *v1.Pod, podNets []*fabricNet) string {
	podName := c.getNameByPod(pod)
	details := make([]string, 0, len(podNets))
	for _, podNet := range podNets {
		details = append(details, fmt.Sprintf(
			"provider=%s subnet=%s ip=%s mac=%s logicalSwitchPort=%s",
			podNet.ProviderName,
			podNet.Subnet.Name,
			pod.Annotations[fmt.Sprintf(util.IPAddressAnnotationTemplate, podNet.ProviderName)],
			pod.Annotations[fmt.Sprintf(util.MacAddressAnnotationTemplate, podNet.ProviderName)],
			ovs.PodNameToPortName(podName, pod.Namespace, podNet.ProviderName),
		))
	}
	return strings.Join(details, "; ")
}

func isStatefulSetPod(pod *v1.Pod) (bool, string, types.UID) {
	for _, owner := range pod.OwnerReferences {
		if owner.Kind == util.KindStatefulSet && strings.HasPrefix(owner.APIVersion, appsv1.SchemeGroupVersion.Group+"/") {
			if strings.HasPrefix(pod.Name, owner.Name) {
				return true, owner.Name, owner.UID
			}
		}
	}
	return false, "", ""
}

func isStatefulSetPodToDel(c kubernetes.Interface, pod *v1.Pod, statefulSetName string, statefulSetUID types.UID) bool {
	sts, err := c.AppsV1().StatefulSets(pod.Namespace).Get(context.Background(), statefulSetName, metav1.GetOptions{})
	if err != nil {
		if k8serrors.IsNotFound(err) {
			klog.Infof("statefulset %s/%s has been deleted", pod.Namespace, statefulSetName)
			return true
		}
		klog.Errorf("failed to get statefulset %s/%s: %v", pod.Namespace, statefulSetName, err)
		return false
	}

	if !sts.DeletionTimestamp.IsZero() {
		klog.Infof("statefulset %s/%s is being deleted", pod.Namespace, statefulSetName)
		return true
	}
	if sts.UID != statefulSetUID {
		klog.Infof("statefulset %s/%s is a newly created one", pod.Namespace, statefulSetName)
		return true
	}

	tempStrs := strings.Split(pod.Name, "-")
	numStr := tempStrs[len(tempStrs)-1]
	index, err := strconv.ParseInt(numStr, 10, 0)
	if err != nil {
		klog.Errorf("failed to parse %s to int", numStr)
		return false
	}

	var startOrdinal int64
	if sts.Spec.Ordinals != nil {
		startOrdinal = int64(sts.Spec.Ordinals.Start)
	}
	if index >= startOrdinal+int64(*sts.Spec.Replicas) {
		klog.Infof("statefulset %s/%s is down scaled", pod.Namespace, statefulSetName)
		return true
	}
	return false
}

func isStatefulSetPodToGC(c kubernetes.Interface, pod *v1.Pod, statefulSetName string, statefulSetUID types.UID) bool {
	sts, err := c.AppsV1().StatefulSets(pod.Namespace).Get(context.Background(), statefulSetName, metav1.GetOptions{})
	if err != nil {
		if k8serrors.IsNotFound(err) {
			klog.Infof("statefulset %s/%s has been deleted", pod.Namespace, statefulSetName)
			return true
		}
		klog.Errorf("failed to get statefulset %s/%s: %v", pod.Namespace, statefulSetName, err)
		return false
	}

	if !sts.DeletionTimestamp.IsZero() {
		klog.Infof("statefulset %s/%s is being deleted", pod.Namespace, statefulSetName)
		return true
	}

	if sts.UID != statefulSetUID {
		klog.Infof("statefulset %s/%s is a newly created one", pod.Namespace, statefulSetName)
		return true
	}

	tempStrs := strings.Split(pod.Name, "-")
	numStr := tempStrs[len(tempStrs)-1]
	index, err := strconv.ParseInt(numStr, 10, 0)
	if err != nil {
		klog.Errorf("failed to parse %s to int", numStr)
		return false
	}

	var startOrdinal int64
	if sts.Spec.Ordinals != nil {
		startOrdinal = int64(sts.Spec.Ordinals.Start)
	}
	if index >= startOrdinal+int64(*sts.Spec.Replicas) {
		klog.Infof("statefulset %s/%s is down scaled", pod.Namespace, statefulSetName)
		if !isPodAlive(pod) {
			return true
		}
	}

	return false
}

func getNodeTunlIP(node *v1.Node) ([]net.IP, error) {
	var nodeTunlIPAddr []net.IP
	nodeTunlIP := node.Annotations[util.IPAddressAnnotation]
	if nodeTunlIP == "" {
		return nil, errors.New("node has no tunnel ip annotation")
	}

	for ip := range strings.SplitSeq(nodeTunlIP, ",") {
		parsed := net.ParseIP(ip)
		if parsed == nil {
			return nil, fmt.Errorf("failed to parse tunnel IP %q on node %s", ip, node.Name)
		}
		nodeTunlIPAddr = append(nodeTunlIPAddr, parsed)
	}
	return nodeTunlIPAddr, nil
}

func getNextHopByTunnelIP(gw []net.IP) string {
	nextHop := gw[0].String()
	if len(gw) == 2 {
		nextHop = gw[0].String() + "," + gw[1].String()
	}
	return nextHop
}

func needAllocateSubnets(pod *v1.Pod, nets []*fabricNet) []*fabricNet {
	if !isPodAlive(pod) {
		return nil
	}

	if pod.Annotations == nil {
		return nets
	}

	migrate := false
	if job, ok := pod.Annotations[kubevirtv1.MigrationJobNameAnnotation]; ok {
		klog.Infof("pod %s/%s is in the migration job %s", pod.Namespace, pod.Name, job)
		migrate = true
	}

	result := make([]*fabricNet, 0, len(nets))
	for _, n := range nets {
		if migrate || pod.Annotations[fmt.Sprintf(util.AllocatedAnnotationTemplate, n.ProviderName)] != "true" {
			result = append(result, n)
		}
	}
	return result
}

func (c *Controller) podNeedSync(pod *v1.Pod) (bool, error) {
	if pod.Annotations == nil {
		return true, nil
	}

	if pod.Annotations[util.RoutedAnnotation] != "true" {
		return true, nil
	}

	attachmentNets, err := c.getPodAttachmentNet(pod)
	if err != nil {
		klog.Error(err)
		return false, err
	}

	podName := c.getNameByPod(pod)
	for _, n := range attachmentNets {
		if pod.Annotations[fmt.Sprintf(util.RoutedAnnotationTemplate, n.ProviderName)] != "true" {
			return true, nil
		}
		ipName := ovs.PodNameToPortName(podName, pod.Namespace, n.ProviderName)
		if _, err = c.ipsLister.Get(ipName); err != nil {
			if !k8serrors.IsNotFound(err) {
				err = fmt.Errorf("failed to get ip %s: %w", ipName, err)
				klog.Error(err)
				return false, err
			}
			klog.Infof("ip %s not found", ipName)

			return true, nil
		}
	}
	return false, nil
}

func needRouteSubnets(pod *v1.Pod, nets []*fabricNet) []*fabricNet {
	if !isPodAlive(pod) {
		return nil
	}

	if pod.Annotations == nil {
		return nets
	}

	result := make([]*fabricNet, 0, len(nets))
	for _, n := range nets {
		if !isOvnSubnet(n.Subnet) {
			continue
		}

		if pod.Annotations[fmt.Sprintf(util.AllocatedAnnotationTemplate, n.ProviderName)] == "true" && pod.Spec.NodeName != "" {
			if pod.Annotations[fmt.Sprintf(util.RoutedAnnotationTemplate, n.ProviderName)] != "true" {
				result = append(result, n)
			}
		}
	}
	return result
}

func (c *Controller) getPodDefaultSubnet(pod *v1.Pod) (*fabricv1.Subnet, error) {
	ignoreSubnetNotExist := !pod.DeletionTimestamp.IsZero()

	if lsName := pod.Annotations[util.LogicalSwitchAnnotation]; lsName != "" {
		subnet, err := c.subnetsLister.Get(lsName)
		if err != nil {
			klog.Errorf("failed to get subnet %s: %v", lsName, err)
			if k8serrors.IsNotFound(err) {
				if ignoreSubnetNotExist {
					klog.Errorf("deleting pod %s/%s default subnet %s already not exist, gc will clean its ip cr", pod.Namespace, pod.Name, lsName)
					return nil, nil
				}
			}
			return nil, err
		}
		return subnet, nil
	}
	if poolName := strings.TrimSpace(pod.Annotations[util.IPPoolAnnotation]); poolName != "" &&
		!strings.ContainsAny(poolName, ",;") && net.ParseIP(poolName) == nil {
		pool, err := c.ippoolLister.Get(poolName)
		if err != nil {
			return nil, fmt.Errorf("failed to get ippool %s: %w", poolName, err)
		}
		subnet, err := c.subnetsLister.Get(pool.Spec.Subnet)
		if err != nil {
			return nil, fmt.Errorf("failed to get subnet %s for ippool %s: %w", pool.Spec.Subnet, poolName, err)
		}
		return subnet, nil
	}

	ns, err := c.namespacesLister.Get(pod.Namespace)
	if err != nil {
		klog.Errorf("failed to get namespace %s: %v", pod.Namespace, err)
		return nil, err
	}
	if len(ns.Annotations) == 0 {
		err = fmt.Errorf("namespace %s network annotations is empty", ns.Name)
		klog.Error(err)
		return nil, err
	}

	subnetNames := ns.Annotations[util.LogicalSwitchAnnotation]
	for subnetName := range strings.SplitSeq(subnetNames, ",") {
		if subnetName == "" {
			err = fmt.Errorf("namespace %s default logical switch is not found", ns.Name)
			klog.Error(err)
			return nil, err
		}
		subnet, err := c.subnetsLister.Get(subnetName)
		if err != nil {
			klog.Errorf("failed to get subnet %s: %v", subnetName, err)
			if k8serrors.IsNotFound(err) {
				if ignoreSubnetNotExist {
					klog.Errorf("deleting pod %s/%s namespace subnet %s already not exist, gc will clean its ip cr", pod.Namespace, pod.Name, subnetName)

					continue
				}
			}
			return nil, err
		}

		switch subnet.Spec.Protocol {
		case fabricv1.ProtocolDual:
			if subnet.Status.V6AvailableIPs.EqualInt64(0) && !c.podCanUseExcludeIPs(pod, subnet) {
				klog.Infof("there's no available ipv6 address in subnet %s, try next one", subnet.Name)
				continue
			}
			fallthrough
		case fabricv1.ProtocolIPv4:
			if subnet.Status.V4AvailableIPs.EqualInt64(0) && !c.podCanUseExcludeIPs(pod, subnet) {
				klog.Infof("there's no available ipv4 address in subnet %s, try next one", subnet.Name)
				continue
			}
		case fabricv1.ProtocolIPv6:
			if subnet.Status.V6AvailableIPs.EqualInt64(0) && !c.podCanUseExcludeIPs(pod, subnet) {
				klog.Infof("there's no available ipv6 address in subnet %s, try next one", subnet.Name)
				continue
			}
		}
		return subnet, nil
	}
	return nil, ipam.ErrNoAvailable
}

func (c *Controller) podCanUseExcludeIPs(pod *v1.Pod, subnet *fabricv1.Subnet) bool {
	if ipAddr := pod.Annotations[util.IPAddressAnnotation]; ipAddr != "" {
		return c.checkIPsInExcludeList(ipAddr, subnet.Spec.ExcludeIps, subnet.Spec.CIDRBlock)
	}
	if ipPool := pod.Annotations[util.IPPoolAnnotation]; ipPool != "" {
		return c.checkIPsInExcludeList(ipPool, subnet.Spec.ExcludeIps, subnet.Spec.CIDRBlock)
	}

	return false
}

func (c *Controller) checkIPsInExcludeList(ips string, excludeIPs []string, cidr string) bool {
	expandedExcludeIPs := util.ExpandExcludeIPs(excludeIPs, cidr)

	for ipAddr := range strings.SplitSeq(strings.TrimSpace(ips), ",") {
		ipAddr = strings.TrimSpace(ipAddr)
		if ipAddr == "" {
			continue
		}

		for _, excludeIP := range expandedExcludeIPs {
			if util.ContainsIPs(excludeIP, ipAddr) {
				klog.V(3).Infof("IP %s is found in exclude IP %s, allowing allocation", ipAddr, excludeIP)
				return true
			}
		}
	}
	return false
}

type providerType int

const (
	providerTypeIPAM providerType = iota
	providerTypeOriginal
)

type fabricNet struct {
	Type               providerType
	ProviderName       string
	Subnet             *fabricv1.Subnet
	IsDefault          bool
	AllowLiveMigration bool
	IPRequest          string
	MacRequest         string
	NadName            string
	NadNamespace       string
	InterfaceName      string
}

func (c *Controller) getPodAttachmentNet(pod *v1.Pod) ([]*fabricNet, error) {
	var multusNets []*nadv1.NetworkSelectionElement
	defaultAttachNetworks := pod.Annotations[util.DefaultNetworkAnnotation]
	if defaultAttachNetworks != "" {
		attachments, err := nadutils.ParseNetworkAnnotation(defaultAttachNetworks, pod.Namespace)
		if err != nil {
			klog.Errorf("failed to parse default attach net for pod '%s', %v", pod.Name, err)
			return nil, err
		}
		multusNets = attachments
	}

	attachNetworks := pod.Annotations[nadv1.NetworkAttachmentAnnot]
	if attachNetworks != "" {
		attachments, err := nadutils.ParseNetworkAnnotation(attachNetworks, pod.Namespace)
		if err != nil {
			klog.Errorf("failed to parse attach net for pod '%s', %v", pod.Name, err)
			return nil, err
		}
		multusNets = append(multusNets, attachments...)
	}

	var subnets []*fabricv1.Subnet
	if len(multusNets) != 0 {
		var err error
		subnets, err = c.subnetsLister.List(labels.Everything())
		if err != nil {
			klog.Errorf("failed to list subnets: %v", err)
			return nil, err
		}
	}

	ignoreSubnetNotExist := !pod.DeletionTimestamp.IsZero()

	nadCounts := make(map[string]int)
	for _, attach := range multusNets {
		nadCounts[fmt.Sprintf("%s/%s", attach.Namespace, attach.Name)]++
	}

	result := make([]*fabricNet, 0, len(multusNets))
	for _, attach := range multusNets {
		nadKey := fmt.Sprintf("%s/%s", attach.Namespace, attach.Name)
		network, err := c.netAttachLister.NetworkAttachmentDefinitions(attach.Namespace).Get(attach.Name)
		if err != nil {
			klog.Errorf("failed to get net-attach-def %s, %v", attach.Name, err)
			if k8serrors.IsNotFound(err) && ignoreSubnetNotExist {
				providerName := fmt.Sprintf("%s.%s.%s", attach.Name, attach.Namespace, util.OvnProvider)

				if nadCounts[nadKey] > 1 && attach.InterfaceRequest != "" {
					providerName = fmt.Sprintf("%s.%s", providerName, attach.InterfaceRequest)
				}

				ipamProviderName := fmt.Sprintf("%s.%s", attach.Name, attach.Namespace)

				subnetName := pod.Annotations[fmt.Sprintf(util.LogicalSwitchAnnotationTemplate, providerName)]
				if subnetName == "" {
					for _, subnet := range subnets {
						if subnet.Spec.Provider == providerName || subnet.Spec.Provider == ipamProviderName {
							subnetName = subnet.Name

							providerName = subnet.Spec.Provider
							break
						}
					}
				}

				if subnetName == "" {
					klog.Errorf("deleting pod %s/%s net-attach-def %s not found and cannot determine subnet, gc will clean its ip cr", pod.Namespace, pod.Name, attach.Name)
					continue
				}

				subnet, err := c.subnetsLister.Get(subnetName)
				if err != nil {
					klog.Errorf("failed to get subnet %s, %v", subnetName, err)
					if k8serrors.IsNotFound(err) {
						klog.Errorf("deleting pod %s/%s attach subnet %s already not exist, gc will clean its ip cr", pod.Namespace, pod.Name, subnetName)
						continue
					}
					return nil, err
				}

				klog.Infof("pod %s/%s net-attach-def %s not found, using subnet %s for cleanup", pod.Namespace, pod.Name, attach.Name, subnetName)
				result = append(result, &fabricNet{
					Type:          providerTypeIPAM,
					ProviderName:  providerName,
					Subnet:        subnet,
					IsDefault:     util.IsDefaultNet(pod.Annotations[util.DefaultNetworkAnnotation], attach),
					NadName:       attach.Name,
					NadNamespace:  attach.Namespace,
					InterfaceName: attach.InterfaceRequest,
				})
				continue
			}
			return nil, err
		}

		if network.Spec.Config == "" {
			continue
		}

		netCfg, err := loadNetConf([]byte(network.Spec.Config))
		if err != nil {
			klog.Errorf("failed to load config of net-attach-def %s, %v", attach.Name, err)
			return nil, err
		}

		var providerName string
		if util.IsOvnNetwork(netCfg) {
			allowLiveMigration := false
			isDefault := util.IsDefaultNet(pod.Annotations[util.DefaultNetworkAnnotation], attach)

			providerName = fmt.Sprintf("%s.%s.%s", attach.Name, attach.Namespace, util.OvnProvider)
			if nadCounts[nadKey] > 1 && attach.InterfaceRequest != "" {
				providerName = fmt.Sprintf("%s.%s", providerName, attach.InterfaceRequest)
			}
			if pod.Annotations[kubevirtv1.MigrationJobNameAnnotation] != "" {
				allowLiveMigration = true
			}

			subnetName := pod.Annotations[fmt.Sprintf(util.LogicalSwitchAnnotationTemplate, providerName)]

			subnetMatches := func(subnet *fabricv1.Subnet, providerName, ifName string) bool {
				var subnetProviderName string

				subnetProviderName, _ = strings.CutSuffix(providerName, "."+ifName)
				klog.Infof("subnet %s, subnet provider %s, providername %s, trimmed subnetprovider %s, ifName %s", subnet.Name, subnet.Spec.Provider, providerName, subnetProviderName, ifName)

				if subnet.Spec.Provider == subnetProviderName {
					klog.Infof("matched to subnet %s", subnet.Name)
					return true
				}

				return false
			}
			if subnetName == "" {
				for _, subnet := range subnets {
					if subnetMatches(subnet, providerName, attach.InterfaceRequest) {
						subnetName = subnet.Name
						break
					}
				}
			}
			klog.V(5).Infof("found subnet %s for provider %s", subnetName, providerName)
			var subnet *fabricv1.Subnet
			if subnetName == "" {
				err = fmt.Errorf("provider %s is not bound to any subnet", providerName)
				if ignoreSubnetNotExist {
					klog.Errorf("deleting pod %s/%s attach %s %v, gc will clean its ip cr", pod.Namespace, pod.Name, attach.Name, err)
					continue
				}
				klog.Error(err)
				return nil, err
			}
			subnet, err = c.subnetsLister.Get(subnetName)
			if err != nil {
				klog.Errorf("failed to get subnet %s, %v", subnetName, err)
				if k8serrors.IsNotFound(err) {
					if ignoreSubnetNotExist {
						klog.Errorf("deleting pod %s/%s attach subnet %s already not exist, gc will clean its ip cr", pod.Namespace, pod.Name, subnetName)

						continue
					}
				}
				return nil, err
			}

			ret := &fabricNet{
				Type:               providerTypeOriginal,
				ProviderName:       providerName,
				Subnet:             subnet,
				IsDefault:          isDefault,
				AllowLiveMigration: allowLiveMigration,
				MacRequest:         attach.MacRequest,
				IPRequest:          strings.Join(attach.IPRequest, ","),
				NadName:            attach.Name,
				NadNamespace:       attach.Namespace,
				InterfaceName:      attach.InterfaceRequest,
			}
			result = append(result, ret)
		} else {
			if !isFabricIPAMNetwork(netCfg) {
				continue
			}
			providerName = fmt.Sprintf("%s.%s", attach.Name, attach.Namespace)
			foundSubnet := false
			for _, subnet := range subnets {
				if subnet.Spec.Provider == providerName {
					result = append(result, &fabricNet{
						Type:          providerTypeIPAM,
						ProviderName:  providerName,
						Subnet:        subnet,
						MacRequest:    attach.MacRequest,
						IPRequest:     strings.Join(attach.IPRequest, ","),
						NadName:       attach.Name,
						NadNamespace:  attach.Namespace,
						InterfaceName: attach.InterfaceRequest,
					})
					foundSubnet = true
					break
				}
			}
			if !foundSubnet {
				err = fmt.Errorf("provider %s is not bound to any subnet", providerName)
				if ignoreSubnetNotExist {
					klog.Errorf("deleting pod %s/%s IPAM network %s %v, gc will clean its ip cr", pod.Namespace, pod.Name, providerName, err)
					continue
				}
				return nil, err
			}
		}
	}
	return result, nil
}

func isFabricIPAMNetwork(netCfg *multustypes.DelegateNetConf) bool {
	if netCfg.Conf.IPAM.Type == util.CniTypeName {
		return true
	}
	for _, plugin := range netCfg.ConfList.Plugins {
		if plugin.IPAM.Type == util.CniTypeName {
			return true
		}
	}
	return false
}

func (c *Controller) validatePodIP(podName, subnetName, ipv4, ipv6 string) (bool, bool, error) {
	subnet, err := c.subnetsLister.Get(subnetName)
	if err != nil {
		klog.Errorf("failed to get subnet %s: %v", subnetName, err)
		return false, false, err
	}

	if subnet.Spec.Vlan == "" && subnet.Spec.Vpc == c.config.ClusterRouter {
		nodes, err := c.nodesLister.List(labels.Everything())
		if err != nil {
			klog.Errorf("failed to list nodes: %v", err)
			return false, false, err
		}

		for _, node := range nodes {
			nodeIPv4, nodeIPv6 := util.GetNodeInternalIP(*node)
			if ipv4 != "" && ipv4 == nodeIPv4 {
				klog.Errorf("IP address (%s) assigned to pod %s is the same with internal IP address of node %s, reallocating...", ipv4, podName, node.Name)
				return false, true, nil
			}
			if ipv6 != "" && ipv6 == nodeIPv6 {
				klog.Errorf("IP address (%s) assigned to pod %s is the same with internal IP address of node %s, reallocating...", ipv6, podName, node.Name)
				return true, false, nil
			}
		}
	}

	return true, true, nil
}

func (c *Controller) acquireMacOnlyAddress(pod *v1.Pod, podNet *fabricNet, key, portName string) (string, string, string, *fabricv1.Subnet, error) {
	klog.Infof("allocating MAC-only address for pod %s in mac-only subnet %s", key, podNet.Subnet.Name)

	var macPointer *string
	if podNet.NadName != "" && podNet.NadNamespace != "" && podNet.InterfaceName != "" {
		if macStr := pod.Annotations[perInterfaceMACAnnotationKey(podNet.NadName, podNet.NadNamespace, podNet.InterfaceName)]; macStr != "" {
			if _, err := net.ParseMAC(macStr); err != nil {
				return "", "", "", podNet.Subnet, err
			}
			macPointer = &macStr
		}
	}
	if macPointer == nil {
		if annoMAC := pod.Annotations[fmt.Sprintf(util.MacAddressAnnotationTemplate, podNet.ProviderName)]; annoMAC != "" {
			if _, err := net.ParseMAC(annoMAC); err != nil {
				return "", "", "", podNet.Subnet, err
			}
			macPointer = &annoMAC
		}
	}

	_, _, mac, err := c.ipam.GetRandomAddress(key, portName, macPointer, podNet.Subnet.Name, "", nil, !podNet.AllowLiveMigration)
	if err != nil {
		klog.Errorf("failed to allocate MAC for pod %s in mac-only subnet %s: %v", key, podNet.Subnet.Name, err)
		return "", "", "", podNet.Subnet, err
	}
	return "", "", mac, podNet.Subnet, nil
}

func podNetRequestedIPFamily(pod *v1.Pod, podNet *fabricNet) string {
	if pod == nil || pod.Annotations == nil {
		return ""
	}
	return util.NormalizeIPFamily(pod.Annotations[fmt.Sprintf(util.IPFamilyAnnotationTemplate, podNet.ProviderName)])
}

func validateRequestedIPFamilyForSubnet(ipFamily string, subnet *fabricv1.Subnet) error {
	if ipFamily == "" || subnet == nil || subnet.Spec.Protocol == fabricv1.ProtocolDual || subnet.Spec.Protocol == fabricv1.ProtocolMac {
		return nil
	}
	if ipFamily != subnet.Spec.Protocol {
		return fmt.Errorf("requested ip family %s does not match subnet %s protocol %s", ipFamily, subnet.Name, subnet.Spec.Protocol)
	}
	return nil
}

func ippoolHasAvailableIPFamily(ippool *fabricv1.IPPool, subnetProtocol, ipFamily string) bool {
	if ipFamily != "" {
		switch ipFamily {
		case fabricv1.ProtocolIPv4:
			return ippool.Status.V4AvailableIPs.Int64() != 0
		case fabricv1.ProtocolIPv6:
			return ippool.Status.V6AvailableIPs.Int64() != 0
		}
	}

	switch subnetProtocol {
	case fabricv1.ProtocolDual:
		return ippool.Status.V4AvailableIPs.Int64() != 0 && ippool.Status.V6AvailableIPs.Int64() != 0
	case fabricv1.ProtocolIPv4:
		return ippool.Status.V4AvailableIPs.Int64() != 0
	default:
		return ippool.Status.V6AvailableIPs.Int64() != 0
	}
}

func (c *Controller) acquireAddress(pod *v1.Pod, podNet *fabricNet) (string, string, string, *fabricv1.Subnet, error) {
	podName := c.getNameByPod(pod)
	key := cache.NewObjectName(pod.Namespace, podName).String()
	portName := ovs.PodNameToPortName(podName, pod.Namespace, podNet.ProviderName)

	if podNet.Subnet.Spec.Vlan != "" && podNet.Subnet.Spec.CIDRBlock == "" {
		return c.acquireMacOnlyAddress(pod, podNet, key, portName)
	}
	requestedIPFamily := podNetRequestedIPFamily(pod, podNet)
	if err := validateRequestedIPFamilyForSubnet(requestedIPFamily, podNet.Subnet); err != nil {
		return "", "", "", podNet.Subnet, err
	}

	var checkVMPod bool
	isStsPod, _, _ := isStatefulSetPod(pod)

	vipName := pod.Annotations[util.VipAnnotation]
	if vipName != "" {
		vip, err := c.virtualIpsLister.Get(vipName)
		if err != nil {
			klog.Errorf("failed to get static vip '%s', %v", vipName, err)
			return "", "", "", podNet.Subnet, err
		}
		if c.config.EnableKeepVMIP {
			checkVMPod, _ = isVMPod(pod)
		}
		if err = c.podReuseVip(vipName, portName, isStsPod || checkVMPod); err != nil {
			return "", "", "", podNet.Subnet, err
		}
		return vip.Status.V4ip, vip.Status.V6ip, vip.Status.Mac, podNet.Subnet, nil
	}

	var macPointer *string
	if podNet.NadName != "" && podNet.NadNamespace != "" && podNet.InterfaceName != "" {
		key := perInterfaceMACAnnotationKey(podNet.NadName, podNet.NadNamespace, podNet.InterfaceName)
		if macStr := pod.Annotations[key]; macStr != "" {
			if _, err := net.ParseMAC(macStr); err != nil {
				return "", "", "", podNet.Subnet, err
			}
			macPointer = &macStr
		}
	}

	if macPointer == nil && isOvnSubnet(podNet.Subnet) {
		annoMAC := pod.Annotations[fmt.Sprintf(util.MacAddressAnnotationTemplate, podNet.ProviderName)]
		if annoMAC != "" {
			if _, err := net.ParseMAC(annoMAC); err != nil {
				return "", "", "", podNet.Subnet, err
			}
			macPointer = &annoMAC
		}
	} else if macPointer == nil {
		macPointer = new("")
	}

	var nsNets []*fabricNet
	ippoolStr := pod.Annotations[fmt.Sprintf(util.IPPoolAnnotationTemplate, podNet.ProviderName)]
	subnetStr := pod.Annotations[fmt.Sprintf(util.LogicalSwitchAnnotationTemplate, podNet.ProviderName)]

	var err error
	if subnetStr != "" {
		nsNets = []*fabricNet{podNet}
	} else if nsNets, err = c.getNsAvailableSubnets(pod, podNet); err != nil {
		klog.Errorf("failed to get available subnets for pod %s/%s, %v", pod.Namespace, pod.Name, err)
		return "", "", "", podNet.Subnet, err
	}

	if ippoolStr == "" && podNet.IsDefault {
		ns, err := c.namespacesLister.Get(pod.Namespace)
		if err != nil {
			klog.Errorf("failed to get namespace %s: %v", pod.Namespace, err)
			return "", "", "", podNet.Subnet, err
		}
		subnetNames := make([]string, 0, len(nsNets))
		for _, net := range nsNets {
			if net.Subnet.Name == subnetStr {
				podNet.Subnet = net.Subnet
				subnetNames = []string{net.Subnet.Name}
				break
			}
			subnetNames = append(subnetNames, net.Subnet.Name)
		}

		if subnetStr == "" || slices.Contains(subnetNames, subnetStr) {
			if ipPoolList, ok := ns.Annotations[util.IPPoolAnnotation]; ok {
				for ipPoolName := range strings.SplitSeq(ipPoolList, ",") {
					ippool, err := c.ippoolLister.Get(ipPoolName)
					if err != nil {
						klog.Errorf("failed to get ippool %s: %v", ipPoolName, err)
						return "", "", "", podNet.Subnet, err
					}

					if !ippoolHasAvailableIPFamily(ippool, podNet.Subnet.Spec.Protocol, requestedIPFamily) {
						continue
					}

					for _, net := range nsNets {
						if net.Subnet.Name == ippool.Spec.Subnet && slices.Contains(subnetNames, net.Subnet.Name) {
							ippoolStr = ippool.Name
							podNet.Subnet = net.Subnet
							break
						}
					}
					if ippoolStr != "" {
						break
					}
				}
				if ippoolStr == "" {
					klog.Infof("no available ippool in subnet(s) %s for pod %s/%s", strings.Join(subnetNames, ","), pod.Namespace, pod.Name)
					return "", "", "", podNet.Subnet, ipam.ErrNoAvailable
				}
			}
		}
	}

	if pod.Annotations[fmt.Sprintf(util.IPAddressAnnotationTemplate, podNet.ProviderName)] == "" &&
		ippoolStr == "" {
		if podNet.NadName != "" && podNet.NadNamespace != "" && podNet.InterfaceName != "" {
			annoKey := perInterfaceIPAnnotationKey(podNet.NadName, podNet.NadNamespace, podNet.InterfaceName)
			if ipStr := pod.Annotations[annoKey]; ipStr != "" {
				return c.acquireStaticAddressHelper(pod, podNet, portName, macPointer, ippoolStr, nsNets, isStsPod, key, requestedIPFamily)
			}
		}

		var skippedAddrs []string
		for {
			ipv4, ipv6, mac, err := c.ipam.GetRandomAddressWithFamily(key, portName, macPointer, podNet.Subnet.Name, "", requestedIPFamily, skippedAddrs, !podNet.AllowLiveMigration)
			if err != nil {
				klog.Error(err)
				return "", "", "", podNet.Subnet, err
			}
			ipv4OK, ipv6OK, err := c.validatePodIP(pod.Name, podNet.Subnet.Name, ipv4, ipv6)
			if err != nil {
				klog.Error(err)
				return "", "", "", podNet.Subnet, err
			}
			if ipv4OK && ipv6OK {
				return ipv4, ipv6, mac, podNet.Subnet, nil
			}

			if !ipv4OK {
				skippedAddrs = append(skippedAddrs, ipv4)
			}
			if !ipv6OK {
				skippedAddrs = append(skippedAddrs, ipv6)
			}
		}
	}

	return c.acquireStaticAddressHelper(pod, podNet, portName, macPointer, ippoolStr, nsNets, isStsPod, key, requestedIPFamily)
}

func (c *Controller) acquireStaticAddressHelper(pod *v1.Pod, podNet *fabricNet, portName string, macPointer *string, ippoolStr string, nsNets []*fabricNet, isStsPod bool, key, requestedIPFamily string) (string, string, string, *fabricv1.Subnet, error) {
	var v4IP, v6IP, mac string
	var err error

	if podNet.NadName != "" && podNet.NadNamespace != "" && podNet.InterfaceName != "" {
		annotationKey := perInterfaceIPAnnotationKey(podNet.NadName, podNet.NadNamespace, podNet.InterfaceName)
		if ipStr := pod.Annotations[annotationKey]; ipStr != "" {
			for _, net := range nsNets {
				v4IP, v6IP, mac, err = c.acquireStaticAddress(key, portName, ipStr, macPointer, net.Subnet.Name, net.AllowLiveMigration, requestedIPFamily)
				if err == nil {
					return v4IP, v6IP, mac, net.Subnet, nil
				}
			}
			return v4IP, v6IP, mac, podNet.Subnet, err
		}
	}

	if ipStr := pod.Annotations[fmt.Sprintf(util.IPAddressAnnotationTemplate, podNet.ProviderName)]; ipStr != "" {
		for _, net := range nsNets {
			v4IP, v6IP, mac, err = c.acquireStaticAddress(key, portName, ipStr, macPointer, net.Subnet.Name, net.AllowLiveMigration, requestedIPFamily)
			if err == nil {
				return v4IP, v6IP, mac, net.Subnet, nil
			}
		}
		return v4IP, v6IP, mac, podNet.Subnet, err
	}

	if ippoolStr != "" {
		var ipPool []string
		if strings.ContainsRune(ippoolStr, ';') {
			ipPool = strings.Split(ippoolStr, ";")
		} else {
			ipPool = strings.Split(ippoolStr, ",")
			if len(ipPool) == 2 && util.CheckProtocol(ipPool[0]) != util.CheckProtocol(ipPool[1]) {
				ipPool = []string{ippoolStr}
			}
		}
		for i, ip := range ipPool {
			ipPool[i] = strings.TrimSpace(ip)
		}

		if len(ipPool) == 1 && (!strings.ContainsRune(ipPool[0], ',') && net.ParseIP(ipPool[0]) == nil) {
			var skippedAddrs []string
			pool, err := c.ippoolLister.Get(ipPool[0])
			if err != nil {
				klog.Errorf("failed to get ippool %s: %v", ipPool[0], err)
				return "", "", "", podNet.Subnet, err
			}
			for {
				ipv4, ipv6, mac, err := c.ipam.GetRandomAddressWithFamily(key, portName, macPointer, pool.Spec.Subnet, ipPool[0], requestedIPFamily, skippedAddrs, !podNet.AllowLiveMigration)
				if err != nil {
					klog.Error(err)
					return "", "", "", podNet.Subnet, err
				}
				ipv4OK, ipv6OK, err := c.validatePodIP(pod.Name, podNet.Subnet.Name, ipv4, ipv6)
				if err != nil {
					klog.Error(err)
					return "", "", "", podNet.Subnet, err
				}
				if ipv4OK && ipv6OK {
					return ipv4, ipv6, mac, podNet.Subnet, nil
				}

				if !ipv4OK {
					skippedAddrs = append(skippedAddrs, ipv4)
				}
				if !ipv6OK {
					skippedAddrs = append(skippedAddrs, ipv6)
				}
			}
		}

		if !isStsPod {
			for _, net := range nsNets {
				for _, staticIP := range ipPool {
					var checkIP string
					ipProtocol := util.CheckProtocol(staticIP)
					if ipProtocol == fabricv1.ProtocolDual {
						checkIP = strings.Split(staticIP, ",")[0]
					} else {
						checkIP = staticIP
					}

					if assignedPod, ok := c.ipam.IsIPAssignedToOtherPod(checkIP, net.Subnet.Name, key); ok {
						klog.Errorf("static address %s for %s has been assigned to %s", staticIP, key, assignedPod)
						err = ipam.ErrConflict
						continue
					}

					v4IP, v6IP, mac, err = c.acquireStaticAddress(key, portName, staticIP, macPointer, net.Subnet.Name, net.AllowLiveMigration, requestedIPFamily)
					if err == nil {
						return v4IP, v6IP, mac, net.Subnet, nil
					}
				}
			}
			klog.Errorf("acquire address from ippool %s for %s failed, %v", ippoolStr, key, err)
			if err != nil {
				return "", "", "", podNet.Subnet, err
			}
		} else {
			tempStrs := strings.Split(pod.Name, "-")
			numStr := tempStrs[len(tempStrs)-1]
			index, _ := strconv.Atoi(numStr)

			if index < len(ipPool) {
				for _, net := range nsNets {
					v4IP, v6IP, mac, err = c.acquireStaticAddress(key, portName, ipPool[index], macPointer, net.Subnet.Name, net.AllowLiveMigration, requestedIPFamily)
					if err == nil {
						return v4IP, v6IP, mac, net.Subnet, nil
					}
				}
				klog.Errorf("acquire address %s for %s failed, %v", ipPool[index], key, err)
				if err != nil {
					return "", "", "", podNet.Subnet, err
				}
			}
		}
	}
	klog.Errorf("allocate address for %s failed, return NoAvailableAddress", key)
	return "", "", "", podNet.Subnet, ipam.ErrNoAvailable
}

func (c *Controller) acquireStaticAddress(key, nicName, ip string, mac *string, subnet string, liveMigration bool, requestedIPFamily string) (string, string, string, error) {
	var v4IP, v6IP, macStr string
	var err error
	for ipStr := range strings.SplitSeq(ip, ",") {
		if net.ParseIP(ipStr) == nil {
			return "", "", "", fmt.Errorf("failed to parse IP %s", ipStr)
		}
	}

	if v4IP, v6IP, macStr, err = c.ipam.GetStaticAddressWithFamily(key, nicName, ip, mac, subnet, requestedIPFamily, !liveMigration); err != nil {
		klog.Errorf("failed to get static ip %v, mac %v, subnet %v, err %v", ip, mac, subnet, err)
		return "", "", "", err
	}
	return v4IP, v6IP, macStr, nil
}

func appendCheckPodNetToDel(c *Controller, pod *v1.Pod, ownerRefName, ownerRefKind string) (bool, []string, error) {
	podNs, err := c.namespacesLister.Get(pod.Namespace)
	if err != nil {
		klog.Errorf("failed to get namespace %s, %v", pod.Namespace, err)
		return false, nil, err
	}

	var ownerRefAnnotations map[string]string
	switch ownerRefKind {
	case util.KindStatefulSet:
		ss, err := c.config.KubeClient.AppsV1().StatefulSets(pod.Namespace).Get(context.Background(), ownerRefName, metav1.GetOptions{})
		if err != nil {
			if k8serrors.IsNotFound(err) {
				klog.Infof("Statefulset %s is not found", ownerRefName)
				return true, nil, nil
			}
			klog.Errorf("failed to get StatefulSet %s, %v", ownerRefName, err)
			return false, nil, err
		}
		if ss.Spec.Template.Annotations != nil {
			ownerRefAnnotations = ss.Spec.Template.Annotations
		}

	case util.KindVirtualMachineInstance:
		vm, err := c.config.KubevirtClient.VirtualMachine(pod.Namespace).Get(context.Background(), ownerRefName, metav1.GetOptions{})
		if err != nil {
			if k8serrors.IsNotFound(err) {
				klog.Infof("VirtualMachine %s is not found", ownerRefName)
				return true, nil, nil
			}
			klog.Errorf("failed to get VirtualMachine %s, %v", ownerRefName, err)
			return false, nil, err
		}
		if vm.Spec.Template != nil &&
			vm.Spec.Template.ObjectMeta.Annotations != nil {
			ownerRefAnnotations = vm.Spec.Template.ObjectMeta.Annotations
		}
	}

	var ipcrToDelete []string
	if defaultIPCRName := appendCheckPodNonMultusNetToDel(c, pod, ownerRefName, ownerRefAnnotations, podNs); defaultIPCRName != "" {
		ipcrToDelete = append(ipcrToDelete, defaultIPCRName)
	}

	if multusIPCRNames := appendCheckPodMultusNetToDel(c, pod, ownerRefName, ownerRefAnnotations); len(multusIPCRNames) != 0 {
		ipcrToDelete = append(ipcrToDelete, multusIPCRNames...)
	}

	return false, ipcrToDelete, nil
}

func appendCheckPodNonMultusNetToDel(c *Controller, pod *v1.Pod, ownerRefName string, ownerRefAnnotations map[string]string, podNs *v1.Namespace) string {
	podDefaultSwitch := strings.TrimSpace(pod.Annotations[util.LogicalSwitchAnnotation])
	if podDefaultSwitch != "" {
		ownerRefSubnet := ownerRefAnnotations[util.LogicalSwitchAnnotation]
		defaultIPCRName := ovs.PodNameToPortName(ownerRefName, pod.Namespace, util.OvnProvider)
		if ownerRefSubnet == "" {
			nsSubnetNames := podNs.Annotations[util.LogicalSwitchAnnotation]

			if nsSubnetNames != "" && !slices.Contains(strings.Split(nsSubnetNames, ","), podDefaultSwitch) {
				klog.Infof("ns %s annotation subnet is %s, which is inconstant with subnet for pod %s, delete pod", pod.Namespace, nsSubnetNames, pod.Name)
				return defaultIPCRName
			}
		} else {
			podIP := pod.Annotations[util.IPAddressAnnotation]
			if shouldCleanPodNet(c, pod, ownerRefName, ownerRefSubnet, podDefaultSwitch, podIP) {
				return defaultIPCRName
			}
		}
	}
	return ""
}

func appendCheckPodMultusNetToDel(c *Controller, pod *v1.Pod, ownerRefName string, ownerRefAnnotations map[string]string) []string {
	var multusIPCRNames []string
	attachmentNets, _ := c.getPodAttachmentNet(pod)
	for _, attachmentNet := range attachmentNets {
		ipCRName := ovs.PodNameToPortName(ownerRefName, pod.Namespace, attachmentNet.ProviderName)
		podSwitch := strings.TrimSpace(pod.Annotations[fmt.Sprintf(util.LogicalSwitchAnnotationTemplate, attachmentNet.ProviderName)])
		ownerRefSubnet := ownerRefAnnotations[fmt.Sprintf(util.LogicalSwitchAnnotationTemplate, attachmentNet.ProviderName)]
		podIP := pod.Annotations[fmt.Sprintf(util.IPAddressAnnotationTemplate, attachmentNet.ProviderName)]
		if shouldCleanPodNet(c, pod, ownerRefName, ownerRefSubnet, podSwitch, podIP) {
			multusIPCRNames = append(multusIPCRNames, ipCRName)
		}
	}
	return multusIPCRNames
}

func shouldCleanPodNet(c *Controller, pod *v1.Pod, ownerRefName, ownerRefSubnet, podSwitch, podIP string) bool {
	podSubnet, err := c.subnetsLister.Get(podSwitch)
	if err != nil {
		if k8serrors.IsNotFound(err) {
			klog.Infof("subnet %s not found for pod %s/%s, not auto clean ip", podSwitch, pod.Namespace, pod.Name)
			return false
		}
		klog.Errorf("failed to get subnet %s, %v, not auto clean ip", podSwitch, err)
		return false
	}
	if podSubnet == nil {
		klog.Errorf("pod %s/%s subnet %s is nil, not auto clean ip", pod.Namespace, pod.Name, podSwitch)
		return false
	}
	if podIP == "" {
		klog.Infof("pod %s/%s annotaions has no ip address, not auto clean ip", pod.Namespace, pod.Name)
		return false
	}
	podSubnetCidr := podSubnet.Spec.CIDRBlock
	if podSubnetCidr == "" {
		klog.Errorf("invalid pod subnet %s empty cidr %s, not auto clean ip", podSwitch, podSubnetCidr)
		return false
	}
	if !util.CIDRContainIP(podSubnetCidr, podIP) {
		klog.Infof("pod's ip %s is not in the range of subnet %s, delete pod", podIP, podSubnet.Name)
		return true
	}

	if ownerRefSubnet != "" && podSubnet.Name != ownerRefSubnet {
		klog.Infof("Subnet of owner %s has been changed from %s to %s, delete pod %s/%s", ownerRefName, podSubnet.Name, ownerRefSubnet, pod.Namespace, pod.Name)
		return true
	}

	return false
}

func (c *Controller) getVMOrphanedAttachmentPorts(namespace, vmName string, existingPorts []ovnnb.LogicalSwitchPort) map[string]bool {
	vm, err := c.config.KubevirtClient.VirtualMachine(namespace).Get(context.Background(), vmName, metav1.GetOptions{})
	if err != nil {
		klog.Errorf("failed to get vm %s/%s for orphaned port detection: %v", namespace, vmName, err)
		return nil
	}

	if vm.Spec.Template == nil {
		return nil
	}

	expectedPorts := make(map[string]bool)
	defaultMultus := false
	hasMultusNetwork := false
	for _, network := range vm.Spec.Template.Spec.Networks {
		if network.Multus == nil {
			continue
		}
		if network.Multus.Default {
			defaultMultus = true
		}
		if network.Multus.NetworkName != "" {
			hasMultusNetwork = true
			items := strings.Split(network.Multus.NetworkName, "/")
			if len(items) != 2 {
				items = []string{namespace, items[0]}
			}
			provider := fmt.Sprintf("%s.%s.%s", items[1], items[0], util.OvnProvider)
			expectedPorts[ovs.PodNameToPortName(vmName, namespace, provider)] = true
		}
	}
	if !defaultMultus {
		expectedPorts[ovs.PodNameToPortName(vmName, namespace, util.OvnProvider)] = true
	}

	if !hasMultusNetwork {
		return nil
	}

	orphanedPorts := make(map[string]bool)
	for _, port := range existingPorts {
		if !expectedPorts[port.Name] {
			klog.Infof("OVN port %s for vm %s/%s is not in VM spec, marking as orphaned",
				port.Name, namespace, vmName)
			orphanedPorts[port.Name] = true
		}
	}

	if len(orphanedPorts) == 0 {
		return nil
	}
	return orphanedPorts
}

func (c *Controller) cleanStaleVMAttachmentIPs(pod *v1.Pod, podName string) {
	podKey := fmt.Sprintf("%s/%s", pod.Namespace, podName)

	ports, err := c.OVNNbClient.ListNormalLogicalSwitchPorts(true, map[string]string{"pod": podKey})
	if err != nil {
		klog.Errorf("failed to list lsps of vm %s for stale cleanup: %v", podKey, err)
		return
	}
	if len(ports) == 0 {
		return
	}

	podNets, err := c.getPodFabricNets(pod)
	if err != nil {
		klog.Errorf("failed to get fabric nets of pod %s for stale cleanup: %v", podKey, err)
		return
	}
	currentPorts := make(map[string]bool, len(podNets)+1)
	for _, podNet := range podNets {
		currentPorts[ovs.PodNameToPortName(podName, pod.Namespace, podNet.ProviderName)] = true
	}
	currentPorts[ovs.PodNameToPortName(podName, pod.Namespace, util.OvnProvider)] = true

	for _, port := range ports {
		if currentPorts[port.Name] {
			continue
		}
		klog.Infof("cleaning stale vm attachment lsp %s (not in current pod networks)", port.Name)
		if err := c.OVNNbClient.DeleteLogicalSwitchPort(port.Name); err != nil {
			klog.Errorf("failed to delete stale lsp %s, skipping IP cleanup to avoid inconsistency: %v", port.Name, err)
			continue
		}

		ipCR, err := c.ipsLister.Get(port.Name)
		if err != nil {
			if !k8serrors.IsNotFound(err) {
				klog.Errorf("failed to get ip %s: %v", port.Name, err)
			}
			continue
		}
		if ipCR.Labels[util.IPReservedLabel] != "true" {
			klog.Infof("deleting stale vm attachment ip CR %s", ipCR.Name)
			if err := c.config.FabricClient.FabricV1().IPs().Delete(context.Background(), ipCR.Name, metav1.DeleteOptions{}); err != nil {
				if !k8serrors.IsNotFound(err) {
					klog.Errorf("failed to delete ip %s: %v", ipCR.Name, err)
				}
			}
			if subnetName := ipCR.Spec.Subnet; subnetName != "" {
				c.ipam.ReleaseAddressByNic(podKey, port.Name, subnetName)
				c.updateSubnetStatusQueue.Add(subnetName)
			}
		}
	}
}

func isVMPod(pod *v1.Pod) (bool, string) {
	for _, owner := range pod.OwnerReferences {
		if owner.Kind == util.KindVirtualMachineInstance &&
			strings.HasPrefix(owner.APIVersion, kubevirtv1.SchemeGroupVersion.Group+"/") {
			return true, owner.Name
		}
	}
	return false, ""
}

func hasAliveSiblingVMPod(pods []*v1.Pod, vmName, excludePodName string) bool {
	for _, p := range pods {
		if p == nil || p.Name == excludePodName {
			continue
		}
		isVM, name := isVMPod(p)
		if !isVM || name != vmName {
			continue
		}
		if isPodAlive(p) {
			return true
		}
	}
	return false
}

func isOwnsByTheVM(vmi metav1.Object) (bool, string) {
	for _, owner := range vmi.GetOwnerReferences() {
		if owner.Kind == util.KindVirtualMachine &&
			strings.HasPrefix(owner.APIVersion, kubevirtv1.SchemeGroupVersion.Group+"/") {
			return true, owner.Name
		}
	}
	return false, ""
}

func (c *Controller) isVMToDel(pod *v1.Pod, vmiName string) bool {
	var (
		vmiAlive bool
		vmName   string
	)

	vmi, err := c.config.KubevirtClient.VirtualMachineInstance(pod.Namespace).Get(context.Background(), vmiName, metav1.GetOptions{})
	if err != nil {
		if k8serrors.IsNotFound(err) {
			vmiAlive = false

			vmName = vmiName
			klog.ErrorS(err, "failed to get vmi, will try to get the vm directly", "name", vmiName)
		} else {
			klog.ErrorS(err, "failed to get vmi", "name", vmiName)
			return false
		}
	} else {
		var ownsByVM bool
		ownsByVM, vmName = isOwnsByTheVM(vmi)
		if !ownsByVM && !vmi.DeletionTimestamp.IsZero() {
			klog.Infof("ephemeral vmi %s is deleting", vmiName)
			return true
		}
		vmiAlive = vmi.DeletionTimestamp.IsZero()
	}

	if vmiAlive {
		return false
	}

	vm, err := c.config.KubevirtClient.VirtualMachine(pod.Namespace).Get(context.Background(), vmName, metav1.GetOptions{})
	if err != nil {
		if k8serrors.IsNotFound(err) {
			klog.ErrorS(err, "failed to get vm", "name", vmName)
			return true
		}
		klog.ErrorS(err, "failed to get vm", "name", vmName)
		return false
	}

	if !vm.DeletionTimestamp.IsZero() {
		klog.Infof("vm %s is deleting", vmName)
		return true
	}
	return false
}

func (c *Controller) getNameByPod(pod *v1.Pod) string {
	if c.config.EnableKeepVMIP {
		if isVMPod, vmName := isVMPod(pod); isVMPod {
			return vmName
		}
	}
	return pod.Name
}

func (c *Controller) getNsAvailableSubnets(pod *v1.Pod, podNet *fabricNet) ([]*fabricNet, error) {
	result := []*fabricNet{podNet}

	ns, err := c.namespacesLister.Get(pod.Namespace)
	if err != nil {
		klog.Errorf("failed to get namespace %s, %v", pod.Namespace, err)
		return nil, err
	}
	if ns.Annotations == nil {
		return []*fabricNet{}, nil
	}

	subnetNames := ns.Annotations[util.LogicalSwitchAnnotation]
	for subnetName := range strings.SplitSeq(subnetNames, ",") {
		if subnetName == "" || subnetName == podNet.Subnet.Name {
			continue
		}
		subnet, err := c.subnetsLister.Get(subnetName)
		if err != nil {
			klog.Errorf("failed to get subnet %v", err)
			return nil, err
		}

		result = append(result, &fabricNet{
			Type:         providerTypeOriginal,
			ProviderName: subnet.Spec.Provider,
			Subnet:       subnet,
		})
	}

	return result, nil
}

func getPodType(pod *v1.Pod) string {
	if ok, _, _ := isStatefulSetPod(pod); ok {
		return util.KindStatefulSet
	}

	if isVMPod, _ := isVMPod(pod); isVMPod {
		return util.KindVirtualMachine
	}
	return ""
}

func (c *Controller) getVirtualIPs(pod *v1.Pod, podNets []*fabricNet) map[string]string {
	vipsListMap := make(map[string][]string)
	var vipNamesList []string
	for vipName := range strings.SplitSeq(strings.TrimSpace(pod.Annotations[util.AAPsAnnotation]), ",") {
		if vipName = strings.TrimSpace(vipName); vipName == "" {
			continue
		}
		if !slices.Contains(vipNamesList, vipName) {
			vipNamesList = append(vipNamesList, vipName)
		} else {
			continue
		}
		vip, err := c.virtualIpsLister.Get(vipName)
		if err != nil {
			klog.Errorf("failed to get vip %s, %v", vipName, err)
			continue
		}
		if vip.Spec.Namespace != pod.Namespace || (vip.Status.V4ip == "" && vip.Status.V6ip == "") {
			continue
		}
		for _, podNet := range podNets {
			if podNet.Subnet.Name == vip.Spec.Subnet {
				key := fmt.Sprintf("%s.%s", podNet.Subnet.Name, podNet.ProviderName)
				vipsList := vipsListMap[key]
				if vipsList == nil {
					vipsList = []string{}
				}

				if util.IsValidIP(vip.Status.V4ip) {
					vipsList = append(vipsList, vip.Status.V4ip)
				}
				if util.IsValidIP(vip.Status.V6ip) {
					vipsList = append(vipsList, vip.Status.V6ip)
				}

				vipsListMap[key] = vipsList
			}
		}
	}

	for _, podNet := range podNets {
		vipStr := pod.Annotations[fmt.Sprintf(util.PortVipAnnotationTemplate, podNet.ProviderName)]
		if vipStr == "" {
			continue
		}
		key := fmt.Sprintf("%s.%s", podNet.Subnet.Name, podNet.ProviderName)
		vipsList := vipsListMap[key]
		if vipsList == nil {
			vipsList = []string{}
		}

		for vip := range strings.SplitSeq(vipStr, ",") {
			if util.IsValidIP(vip) && !slices.Contains(vipsList, vip) {
				vipsList = append(vipsList, vip)
			}
		}

		vipsListMap[key] = vipsList
	}

	vipsMap := make(map[string]string)
	for key, vipsList := range vipsListMap {
		vipsMap[key] = strings.Join(vipsList, ",")
	}
	return vipsMap
}

func perInterfaceIPAnnotationKey(nadName, nadNamespace, ifaceName string) string {
	return fmt.Sprintf("%s.%s.cloudyfolks.io/ip_address.%s", nadName, nadNamespace, ifaceName)
}

func perInterfaceMACAnnotationKey(nadName, nadNamespace, ifaceName string) string {
	return fmt.Sprintf("%s.%s.cloudyfolks.io/mac_address.%s", nadName, nadNamespace, ifaceName)
}
