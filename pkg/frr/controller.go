package frr

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"slices"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	kubeinformers "k8s.io/client-go/informers"
	listerv1 "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/retry"
	"k8s.io/klog/v2"

	kubeovnv1 "github.com/cloudyfolks-labs/fabric/pkg/apis/kubeovn/v1"
	kubeovninformer "github.com/cloudyfolks-labs/fabric/pkg/client/informers/externalversions"
	kubeovnlister "github.com/cloudyfolks-labs/fabric/pkg/client/listers/kubeovn/v1"
	"github.com/cloudyfolks-labs/fabric/pkg/util"
)

type Controller struct {
	config  *Configuration
	applier *Applier

	bgpConfLister kubeovnlister.BgpConfLister
	bgpConfSynced cache.InformerSynced
	vpcLister     kubeovnlister.VpcLister
	vpcSynced     cache.InformerSynced
	ovnEipLister  kubeovnlister.OvnEipLister
	ovnEipSynced  cache.InformerSynced
	lbPoolLister  kubeovnlister.LoadBalancerPoolLister
	lbPoolSynced  cache.InformerSynced
	subnetLister  kubeovnlister.SubnetLister
	subnetSynced  cache.InformerSynced
	nodeLister    listerv1.NodeLister
	nodeSynced    cache.InformerSynced
	podLister     listerv1.PodLister
	podSynced     cache.InformerSynced

	informerFactory        kubeinformers.SharedInformerFactory
	podInformerFactory     kubeinformers.SharedInformerFactory
	kubeovnInformerFactory kubeovninformer.SharedInformerFactory

	trigger chan struct{}
}

func NewController(config *Configuration) (*Controller, error) {
	informerFactory := kubeinformers.NewSharedInformerFactoryWithOptions(config.KubeClient, config.ResyncInterval,
		kubeinformers.WithTransform(util.TrimManagedFields),
		kubeinformers.WithTweakListOptions(func(listOption *metav1.ListOptions) {
			listOption.FieldSelector = "metadata.name=" + config.NodeName
			listOption.AllowWatchBookmarks = true
		}))
	podInformerFactory := kubeinformers.NewSharedInformerFactoryWithOptions(config.KubeClient, config.ResyncInterval,
		kubeinformers.WithTransform(util.TrimManagedFields),
		kubeinformers.WithTweakListOptions(func(listOption *metav1.ListOptions) {
			listOption.FieldSelector = "spec.nodeName=" + config.NodeName
			listOption.AllowWatchBookmarks = true
		}))
	kubeovnInformerFactory := kubeovninformer.NewSharedInformerFactoryWithOptions(config.KubeOvnClient, config.ResyncInterval,
		kubeovninformer.WithTransform(util.TrimManagedFields),
		kubeovninformer.WithTweakListOptions(func(listOption *metav1.ListOptions) {
			listOption.AllowWatchBookmarks = true
		}))

	bgpConfInformer := kubeovnInformerFactory.Fabric().V1().BgpConves()
	vpcInformer := kubeovnInformerFactory.Fabric().V1().Vpcs()
	ovnEipInformer := kubeovnInformerFactory.Fabric().V1().OvnEips()
	lbPoolInformer := kubeovnInformerFactory.Fabric().V1().LoadBalancerPools()
	subnetInformer := kubeovnInformerFactory.Fabric().V1().Subnets()
	nodeInformer := informerFactory.Core().V1().Nodes()
	podInformer := podInformerFactory.Core().V1().Pods()

	c := &Controller{
		config:                 config,
		applier:                NewApplier(config.FrrDir),
		bgpConfLister:          bgpConfInformer.Lister(),
		bgpConfSynced:          bgpConfInformer.Informer().HasSynced,
		vpcLister:              vpcInformer.Lister(),
		vpcSynced:              vpcInformer.Informer().HasSynced,
		ovnEipLister:           ovnEipInformer.Lister(),
		ovnEipSynced:           ovnEipInformer.Informer().HasSynced,
		lbPoolLister:           lbPoolInformer.Lister(),
		lbPoolSynced:           lbPoolInformer.Informer().HasSynced,
		subnetLister:           subnetInformer.Lister(),
		subnetSynced:           subnetInformer.Informer().HasSynced,
		nodeLister:             nodeInformer.Lister(),
		nodeSynced:             nodeInformer.Informer().HasSynced,
		podLister:              podInformer.Lister(),
		podSynced:              podInformer.Informer().HasSynced,
		informerFactory:        informerFactory,
		podInformerFactory:     podInformerFactory,
		kubeovnInformerFactory: kubeovnInformerFactory,
		trigger:                make(chan struct{}, 1),
	}

	handler := cache.ResourceEventHandlerFuncs{
		AddFunc:    func(any) { c.requestReconcile() },
		UpdateFunc: func(any, any) { c.requestReconcile() },
		DeleteFunc: func(any) { c.requestReconcile() },
	}
	for _, informer := range []cache.SharedIndexInformer{
		bgpConfInformer.Informer(),
		vpcInformer.Informer(),
		ovnEipInformer.Informer(),
		lbPoolInformer.Informer(),
		subnetInformer.Informer(),
		nodeInformer.Informer(),
	} {
		if _, err := informer.AddEventHandler(handler); err != nil {
			return nil, err
		}
	}

	return c, nil
}

func (c *Controller) requestReconcile() {
	select {
	case c.trigger <- struct{}{}:
	default:
	}
}

func (c *Controller) Run(ctx context.Context) error {
	c.informerFactory.Start(ctx.Done())
	c.podInformerFactory.Start(ctx.Done())
	c.kubeovnInformerFactory.Start(ctx.Done())

	if !cache.WaitForCacheSync(ctx.Done(), c.bgpConfSynced, c.vpcSynced, c.ovnEipSynced, c.lbPoolSynced, c.subnetSynced, c.nodeSynced, c.podSynced) {
		return errors.New("failed to wait for caches to sync")
	}
	klog.Info("caches synced, starting reconcile loop")

	reassert := time.NewTicker(c.config.ReassertInterval)
	defer reassert.Stop()

	c.requestReconcile()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-c.trigger:
			time.Sleep(c.config.DebounceInterval)
			c.drainTrigger()
			c.reconcile()
		case <-reassert.C:
			c.reconcile()
		}
	}
}

func (c *Controller) drainTrigger() {
	select {
	case <-c.trigger:
	default:
	}
}

func nodeApplyState(serial string, st ApplyStatus) (string, string) {
	switch {
	case st.AppliedSerial == serial:
		return kubeovnv1.BgpNodeStateApplied, ""
	case st.ResultSerial == serial && st.ResultState == "error":
		return kubeovnv1.BgpNodeStateFailed, st.Detail
	default:
		return kubeovnv1.BgpNodeStatePending, "waiting for the FRR reload"
	}
}

func (c *Controller) reconcile() {
	conf, err := c.selectedBgpConf()
	if err != nil {
		klog.Errorf("failed to select the bgp-conf: %v", err)
		return
	}

	input, err := c.desiredInput()
	if err != nil {
		klog.Errorf("failed to compute desired FRR configuration: %v", err)
		if conf != nil {
			c.reportNodeState(conf.Name, kubeovnv1.BgpNodeStateFailed, "", err.Error())
		}
		return
	}
	if c.config.EnableMetrics {
		c.refreshMetrics(input)
	}

	s, err := c.applier.Apply(Render(input))
	if err != nil {
		klog.Errorf("failed to apply FRR configuration: %v", err)
		if conf != nil {
			c.reportNodeState(conf.Name, kubeovnv1.BgpNodeStateFailed, "", err.Error())
		}
		return
	}

	st := c.applier.Status()
	state, message := nodeApplyState(s, st)
	switch state {
	case kubeovnv1.BgpNodeStateApplied:
		klog.V(5).Infof("FRR configuration %s applied", s)
	case kubeovnv1.BgpNodeStateFailed:
		klog.Errorf("FRR reload failed for configuration %s: %s", s, st.Detail)
	default:
		klog.V(3).Infof("FRR configuration %s pending, applied %s", s, st.AppliedSerial)
	}
	if conf != nil {
		c.reportNodeState(conf.Name, state, s, message)
	}
}

func (c *Controller) selectedBgpConf() (*kubeovnv1.BgpConf, error) {
	node, err := c.nodeLister.Get(c.config.NodeName)
	if err != nil {
		return nil, fmt.Errorf("failed to get node %s: %w", c.config.NodeName, err)
	}
	return c.selectBgpConf(node)
}

func (c *Controller) reportNodeState(confName, state, serial, message string) {
	confs := c.config.KubeOvnClient.FabricV1().BgpConves()
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		conf, err := confs.Get(context.Background(), confName, metav1.GetOptions{})
		if err != nil {
			if k8serrors.IsNotFound(err) {
				return nil
			}
			return err
		}
		newConf := conf.DeepCopy()
		nodes := make([]kubeovnv1.BgpNodeStatus, 0, len(newConf.Status.Nodes)+1)
		var current *kubeovnv1.BgpNodeStatus
		for i := range newConf.Status.Nodes {
			if newConf.Status.Nodes[i].Node == c.config.NodeName {
				current = &newConf.Status.Nodes[i]
				continue
			}
			nodes = append(nodes, newConf.Status.Nodes[i])
		}
		if current != nil && current.State == state && current.Serial == serial && current.Message == message {
			return nil
		}
		nodes = append(nodes, kubeovnv1.BgpNodeStatus{
			Node:           c.config.NodeName,
			Serial:         serial,
			State:          state,
			Message:        message,
			LastUpdateTime: metav1.Now(),
		})
		sort.Slice(nodes, func(i, j int) bool { return nodes[i].Node < nodes[j].Node })
		newConf.Status.Nodes = nodes
		_, err = confs.UpdateStatus(context.Background(), newConf, metav1.UpdateOptions{})
		return err
	})
	if err != nil {
		klog.Errorf("failed to report the FRR state of node %s on bgp-conf %s: %v", c.config.NodeName, confName, err)
	}
}

func (c *Controller) desiredInput() (RenderInput, error) {
	node, err := c.nodeLister.Get(c.config.NodeName)
	if err != nil {
		return RenderInput{}, fmt.Errorf("failed to get node %s: %w", c.config.NodeName, err)
	}

	conf, err := c.selectBgpConf(node)
	if err != nil {
		return RenderInput{}, err
	}
	if conf == nil {
		return RenderInput{NodeName: c.config.NodeName}, nil
	}

	routerID := conf.Spec.RouterID
	if routerID == "" {
		routerID, _ = util.GetNodeInternalIP(*node)
	}
	if routerID == "" {
		return RenderInput{}, fmt.Errorf("node %s has no IPv4 internal address, set spec.routerId on bgp-conf %s", c.config.NodeName, conf.Name)
	}

	vpcs, err := c.collectVpcAdvertisements()
	if err != nil {
		return RenderInput{}, err
	}

	hostRoutes, err := c.collectHostRouteEntries()
	if err != nil {
		return RenderInput{}, err
	}
	input := BuildRenderInput(conf, c.config.NodeName, routerID, vpcs)
	input.AdvertiseFilter = mergeAdvertiseEntries(input.AdvertiseFilter, hostRoutes)
	if err = ValidateRenderInput(input); err != nil {
		return RenderInput{}, fmt.Errorf("invalid configuration from bgp-conf %s: %w", conf.Name, err)
	}
	return input, nil
}

func (c *Controller) refreshMetrics(input RenderInput) {
	neighbors := make([]string, 0, len(input.Neighbors))
	for _, n := range input.Neighbors {
		neighbors = append(neighbors, n.Address)
	}
	peers, prefixes := c.bgpState(input.LocalASN != 0)
	setMetrics(metricsInput{
		node:        c.config.NodeName,
		neighbors:   neighbors,
		vpcs:        input.Vpcs,
		peers:       peers,
		prefixes:    prefixes,
		tableRoutes: tableRouteCount,
	})
}

func (c *Controller) bgpState(bgpRendered bool) (map[string]BgpPeer, map[string]int) {
	peers := map[string]BgpPeer{}
	prefixes := map[string]int{}
	if !bgpRendered {
		return peers, prefixes
	}
	if data, ok := readSnapshot(c.config.FrrDir, bgpSummaryFileName); ok {
		parsed, err := ParseBgpSummary(data)
		if err != nil {
			klog.Warningf("failed to read the bgp summary snapshot: %v", err)
		} else {
			peers = parsed
		}
	}
	if data, ok := readSnapshot(c.config.FrrDir, bgpRoutesFileName); ok {
		parsed, err := CountPrefixesByNextHop(data)
		if err != nil {
			klog.Warningf("failed to read the bgp routes snapshot: %v", err)
		} else {
			prefixes = parsed
		}
	}
	return peers, prefixes
}

func (c *Controller) selectBgpConf(node *corev1.Node) (*kubeovnv1.BgpConf, error) {
	confs, err := c.bgpConfLister.List(labels.Everything())
	if err != nil {
		return nil, fmt.Errorf("failed to list bgp-confs: %w", err)
	}

	var matched []*kubeovnv1.BgpConf
	for _, conf := range confs {
		if len(conf.Spec.NodeSelector) == 0 {
			continue
		}
		if labels.SelectorFromSet(conf.Spec.NodeSelector).Matches(labels.Set(node.Labels)) {
			matched = append(matched, conf)
		}
	}
	if len(matched) == 0 {
		return nil, nil
	}
	sort.Slice(matched, func(i, j int) bool { return matched[i].Name < matched[j].Name })
	if len(matched) > 1 {
		names := make([]string, 0, len(matched))
		for _, conf := range matched {
			names = append(names, conf.Name)
		}
		klog.Warningf("multiple bgp-confs match node %s: %s, using %s", c.config.NodeName, strings.Join(names, ", "), matched[0].Name)
	}
	return matched[0], nil
}

func (c *Controller) collectVpcAdvertisements() ([]VpcAdvertisement, error) {
	vpcs, err := c.vpcLister.List(labels.Everything())
	if err != nil {
		return nil, fmt.Errorf("failed to list vpcs: %w", err)
	}
	eips, err := c.ovnEipLister.List(labels.Everything())
	if err != nil {
		return nil, fmt.Errorf("failed to list ovn-eips: %w", err)
	}
	return vpcAdvertisements(vpcs, eips, netDevicePresent), nil
}

func netDevicePresent(name string) bool {
	_, err := os.Stat("/sys/class/net/" + name)
	return err == nil
}

func vrfDeviceName(dr *kubeovnv1.VpcDynamicRouting) string {
	if dr.VrfName != "" {
		return dr.VrfName
	}
	return fmt.Sprintf("ovnvrf%d", dr.VrfID)
}

func vpcAdvertisements(vpcs []*kubeovnv1.Vpc, eips []*kubeovnv1.OvnEip, devicePresent func(string) bool) []VpcAdvertisement {
	result := make([]VpcAdvertisement, 0, len(vpcs))
	for _, vpc := range vpcs {
		dr := vpc.Spec.DynamicRouting
		if !dr.IsEnabled() {
			continue
		}
		if dr.VrfID == 0 {
			klog.Warningf("vpc %s has dynamic routing enabled without an explicit vrfId, skipping advertisement", vpc.Name)
			continue
		}
		if device := vrfDeviceName(dr); devicePresent(device) {
			klog.Warningf("vpc %s: vrf device %s owns table %d and FRR table-direct does not follow routes of a vrf table, set maintainVrf to false, skipping advertisement", vpc.Name, device, dr.VrfID)
			continue
		}
		lrpIP, err := lrpAddress(eips, vpc)
		if err != nil {
			klog.Warningf("vpc %s: %v, skipping advertisement", vpc.Name, err)
			continue
		}
		result = append(result, VpcAdvertisement{
			VpcName: vpc.Name,
			TableID: dr.VrfID,
			LrpIP:   lrpIP,
		})
	}
	return result
}

func (c *Controller) collectHostRouteEntries() ([]string, error) {
	pools, err := c.lbPoolLister.List(labels.Everything())
	if err != nil {
		return nil, fmt.Errorf("failed to list loadbalancer-pools: %w", err)
	}
	eips, err := c.ovnEipLister.List(labels.Everything())
	if err != nil {
		return nil, fmt.Errorf("failed to list ovn-eips: %w", err)
	}
	subnets := make([]*kubeovnv1.Subnet, 0)
	for _, name := range advertisedSubnetNames(pools, eips) {
		subnet, err := c.subnetLister.Get(name)
		if err != nil {
			klog.Warningf("subnet %s not found, skipping its host routes in the advertise filter", name)
			continue
		}
		subnets = append(subnets, subnet)
	}
	return hostRouteEntries(subnets), nil
}

func advertisedSubnetNames(pools []*kubeovnv1.LoadBalancerPool, eips []*kubeovnv1.OvnEip) []string {
	names := make(map[string]struct{})
	for _, pool := range pools {
		if pool.Spec.Announce == kubeovnv1.LoadBalancerPoolAnnounceBGP {
			names[pool.Spec.Subnet] = struct{}{}
		}
	}
	for _, eip := range eips {
		if eip.Spec.Type == util.OvnEipTypeNAT && eip.Spec.ExternalSubnet != "" {
			names[eip.Spec.ExternalSubnet] = struct{}{}
		}
	}
	return slices.Sorted(maps.Keys(names))
}

func hostRouteEntries(subnets []*kubeovnv1.Subnet) []string {
	entries := make([]string, 0, len(subnets))
	for _, subnet := range subnets {
		for cidr := range strings.SplitSeq(subnet.Spec.CIDRBlock, ",") {
			if cidr == "" || util.CheckProtocol(cidr) != kubeovnv1.ProtocolIPv4 {
				continue
			}
			entries = append(entries, cidr+" ge 32 le 32")
		}
	}
	sort.Strings(entries)
	return entries
}

func mergeAdvertiseEntries(base, extra []string) []string {
	merged := slices.Clone(base)
	for _, entry := range extra {
		if !slices.Contains(merged, entry) {
			merged = append(merged, entry)
		}
	}
	return merged
}

func lrpAddress(eips []*kubeovnv1.OvnEip, vpc *kubeovnv1.Vpc) (string, error) {
	if subnet := vpc.Spec.DynamicRouting.ExternalSubnet; subnet != "" {
		for _, eip := range eips {
			if eip.Spec.Type == util.OvnEipTypeLRP && eip.Spec.ExternalSubnet == subnet && eip.Name == vpc.Name+"-"+subnet && eip.Status.V4Ip != "" {
				return eip.Status.V4Ip, nil
			}
		}
		return "", fmt.Errorf("no ready lrp ovn-eip on external subnet %s named by dynamicRouting.externalSubnet", subnet)
	}
	var candidates []string
	for _, eip := range eips {
		if eip.Spec.Type != util.OvnEipTypeLRP || eip.Spec.ExternalSubnet == "" || eip.Status.V4Ip == "" {
			continue
		}
		if eip.Name != vpc.Name+"-"+eip.Spec.ExternalSubnet {
			continue
		}
		if len(vpc.Spec.ExtraExternalSubnets) == 1 && eip.Spec.ExternalSubnet != vpc.Spec.ExtraExternalSubnets[0] {
			continue
		}
		candidates = append(candidates, eip.Status.V4Ip)
	}
	switch len(candidates) {
	case 0:
		return "", errors.New("no ready lrp ovn-eip on the external subnet")
	case 1:
		return candidates[0], nil
	default:
		return "", fmt.Errorf("%d gateway lrps, dynamicRouting.externalSubnet must name the one whose lrp is the bgp next hop", len(candidates))
	}
}
