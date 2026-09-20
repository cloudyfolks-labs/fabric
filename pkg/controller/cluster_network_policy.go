package controller

import (
	"errors"
	"fmt"
	"net"
	"reflect"
	"strings"
	"unicode"

	"k8s.io/client-go/tools/cache"

	"github.com/scylladb/go-set/strset"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/klog/v2"

	fabricv1 "github.com/cloudyfolks-labs/fabric/pkg/apis/fabric/v1"
	"github.com/cloudyfolks-labs/fabric/pkg/ovsdb/ovnnb"
	"github.com/cloudyfolks-labs/fabric/pkg/util"

	"sigs.k8s.io/network-policy-api/apis/v1alpha2"
)

type ClusterNetworkPolicyChangedDelta struct {
	key              string
	ruleNames        [util.CnpMaxRules]ChangedName
	field            ChangedField
	DNSReconcileDone bool
}

func (c *Controller) enqueueAddCnp(obj any) {
	key := cache.MetaObjectToName(obj.(*v1alpha2.ClusterNetworkPolicy)).String()
	klog.V(3).Infof("enqueue add cnp %s", key)
	c.addCnpQueue.Add(key)
}

func (c *Controller) enqueueUpdateCnp(oldObj, newObj any) {
	oldCnp := oldObj.(*v1alpha2.ClusterNetworkPolicy)
	newCnp := newObj.(*v1alpha2.ClusterNetworkPolicy)

	if shouldRecreateCnpACLs(oldCnp, newCnp) {
		c.addCnpQueue.Add(newCnp.Name)
		return
	}

	klog.V(3).Infof("enqueue update cnp %s", newCnp.Name)

	if shouldUpdateCnpPortGroup(oldCnp, newCnp) {
		c.updateCnpQueue.Add(&ClusterNetworkPolicyChangedDelta{key: newCnp.Name, field: ChangedSubject})
	}

	changedIngressRuleNames, changedEgressRuleNames := getCnpAddressSetsToUpdate(oldCnp, newCnp)

	if !isCnpRulesArrayEmpty(changedIngressRuleNames) {
		c.updateCnpQueue.Add(&ClusterNetworkPolicyChangedDelta{
			key:       newCnp.Name,
			ruleNames: changedIngressRuleNames,
			field:     ChangedIngressRule,
		})
	}

	if !isCnpRulesArrayEmpty(changedEgressRuleNames) {
		c.updateCnpQueue.Add(&ClusterNetworkPolicyChangedDelta{
			key:       newCnp.Name,
			ruleNames: changedEgressRuleNames,
			field:     ChangedEgressRule,
		})
	}
}

func (c *Controller) enqueueDeleteCnp(obj any) {
	var cnp *v1alpha2.ClusterNetworkPolicy
	switch t := obj.(type) {
	case *v1alpha2.ClusterNetworkPolicy:
		cnp = t
	case cache.DeletedFinalStateUnknown:
		a, ok := t.Obj.(*v1alpha2.ClusterNetworkPolicy)
		if !ok {
			klog.Warningf("unexpected object type: %T", t.Obj)
			return
		}
		cnp = a
	default:
		klog.Warningf("unexpected type: %T", obj)
		return
	}

	klog.V(3).Infof("enqueue delete cnp %s", cache.MetaObjectToName(cnp).String())
	c.deleteCnpQueue.Add(cnp)
}

func (c *Controller) handleAddCnp(key string) (err error) {
	c.cnpKeyMutex.LockKey(key)
	defer func() { _ = c.cnpKeyMutex.UnlockKey(key) }()

	cachedCnp, err := c.cnpsLister.Get(key)
	if err != nil {
		if k8serrors.IsNotFound(err) {
			return nil
		}
		klog.Error(err)
		return err
	}
	klog.Infof("handle add cnp %s", cachedCnp.Name)
	cnp := cachedCnp.DeepCopy()

	c.priorityMapMutex.Lock()
	if err := c.validateCnpConfig(cnp); err != nil {
		c.priorityMapMutex.Unlock()
		err := fmt.Errorf("failed to validate cnp %s: %w", cnp.Name, err)
		klog.Error(err)
		return err
	}

	if err := c.updateCnpPriorityMapEntries(cnp); err != nil {
		c.priorityMapMutex.Unlock()
		err := fmt.Errorf("failed to update priority maps for cnp %s: %w", cnp.Name, err)
		klog.Error(err)
		return err
	}
	c.priorityMapMutex.Unlock()

	var logActions []string
	if cnp.Annotations[util.ACLActionsLogAnnotation] != "" {
		logActions = strings.Split(cnp.Annotations[util.ACLActionsLogAnnotation], ",")
	}

	if err := c.setupCnpPortGroup(cnp); err != nil {
		klog.Errorf("failed to create port group for cnp %s: %v", cnp.Name, err)
		return err
	}

	cnpName := getCnpName(cnp.Name)
	pgName := getCnpPortGroupName(cnp)
	cnpACLTier := getCnpACLTier(cnp.Spec.Tier)

	curIngressAddrSet, curEgressAddrSet, err := c.getCnpCurrentAddrSetByName(cnpName)
	if err != nil {
		klog.Errorf("failed to list address sets for cnp %s: %v", cnp.Name, err)
		return err
	}

	desiredIngressAddrSet := strset.NewWithSize(len(cnp.Spec.Ingress) * 2)
	desiredEgressAddrSet := strset.NewWithSize(len(cnp.Spec.Egress) * 2)

	ingressACLOps, err := c.OVNNbClient.DeleteAclsOps(pgName, portGroupKey, "to-lport", nil)
	if err != nil {
		klog.Errorf("failed to generate clear operations for cnp %s ingress acls: %v", cnp.Name, err)
		return err
	}

	for index, rule := range cnp.Spec.Ingress {
		v4AddressSetName, as4len, v6AddressSetName, as6len, err := c.generateCnpIngressAddressSet(cnpName, pgName, rule, index)
		if err != nil {
			err := fmt.Errorf("failed to generate ingress address set for cnp %s: %w", cnp.Name, err)
			klog.Error(err)
			return err
		}

		desiredIngressAddrSet.Add(v4AddressSetName, v6AddressSetName)

		aclPriority := getCnpACLPriority(cnp, index)
		rulePorts := []v1alpha2.ClusterNetworkPolicyPort{}
		if rule.Ports != nil {
			rulePorts = *rule.Ports
		}

		if as4len != 0 {
			aclName := getCnpACLName(cnpName, fabricv1.ProtocolIPv4, "ingress", index)
			ops, err := c.OVNNbClient.UpdateCnpRuleACLOps(pgName, v4AddressSetName, fabricv1.ProtocolIPv4, aclName, aclPriority, getCnpACLAction(rule.Action), logActions, rulePorts, true, cnpACLTier)
			if err != nil {
				klog.Errorf("failed to add v4 ingress acls for cnp %s: %v", key, err)
				return err
			}
			ingressACLOps = append(ingressACLOps, ops...)
		}

		if as6len != 0 {
			aclName := getCnpACLName(cnpName, fabricv1.ProtocolIPv6, "ingress", index)
			ops, err := c.OVNNbClient.UpdateCnpRuleACLOps(pgName, v6AddressSetName, fabricv1.ProtocolIPv6, aclName, aclPriority, getCnpACLAction(rule.Action), logActions, rulePorts, true, cnpACLTier)
			if err != nil {
				klog.Errorf("failed to add v6 ingress acls for cnp %s: %v", cnp.Name, err)
				return err
			}
			ingressACLOps = append(ingressACLOps, ops...)
		}
	}

	if err := c.OVNNbClient.Transact("add-ingress-acls", ingressACLOps); err != nil {
		return fmt.Errorf("failed to add ingress acls for cnp %s: %w", cnp.Name, err)
	}
	if err := c.deleteUnusedAddrSetForAnp(curIngressAddrSet, desiredIngressAddrSet); err != nil {
		return fmt.Errorf("failed to delete unused ingress address set for cnp %s: %w", cnp.Name, err)
	}

	egressACLOps, err := c.OVNNbClient.DeleteAclsOps(pgName, portGroupKey, "from-lport", nil)
	if err != nil {
		klog.Errorf("failed to generate clear operations for cnp %s egress acls: %v", cnp.Name, err)
		return err
	}

	c.domainResolver.setPolicyDomains(cnpName, getCnpDomainsNames(cnp))

	hasDomainNames := hasCnpDomainNames(cnp)

	for index, rule := range cnp.Spec.Egress {
		v4AddressSetName, as4len, v6AddressSetName, as6len, err := c.generateCnpEgressAddressSet(cnpName, pgName, rule, index)
		if err != nil {
			err := fmt.Errorf("failed to generate egress address set for cnp %s: %w", cnp.Name, err)
			klog.Error(err)
			return err
		}

		desiredEgressAddrSet.Add(v4AddressSetName, v6AddressSetName)

		aclPriority := getCnpACLPriority(cnp, index)
		rulePorts := []v1alpha2.ClusterNetworkPolicyPort{}
		if rule.Ports != nil {
			rulePorts = *rule.Ports
		}

		if as4len != 0 || hasDomainNames {
			aclName := getCnpACLName(cnpName, fabricv1.ProtocolIPv4, "egress", index)
			ops, err := c.OVNNbClient.UpdateCnpRuleACLOps(pgName, v4AddressSetName, fabricv1.ProtocolIPv4, aclName, aclPriority, getCnpACLAction(rule.Action), logActions, rulePorts, false, cnpACLTier)
			if err != nil {
				klog.Errorf("failed to add v4 egress acls for cnp %s: %v", key, err)
				return err
			}
			egressACLOps = append(egressACLOps, ops...)
		}

		if as6len != 0 || hasDomainNames {
			aclName := getCnpACLName(cnpName, fabricv1.ProtocolIPv6, "egress", index)
			ops, err := c.OVNNbClient.UpdateCnpRuleACLOps(pgName, v6AddressSetName, fabricv1.ProtocolIPv6, aclName, aclPriority, getCnpACLAction(rule.Action), logActions, rulePorts, false, cnpACLTier)
			if err != nil {
				klog.Errorf("failed to add v6 egress acls for cnp %s: %v", key, err)
				return err
			}
			egressACLOps = append(egressACLOps, ops...)
		}
	}

	if err := c.OVNNbClient.Transact("add-egress-acls", egressACLOps); err != nil {
		return fmt.Errorf("failed to add egress acls for cnp %s: %w", key, err)
	}
	if err := c.deleteUnusedAddrSetForAnp(curEgressAddrSet, desiredEgressAddrSet); err != nil {
		return fmt.Errorf("failed to delete unused egress address set for cnp %s: %w", key, err)
	}

	return nil
}

func (c *Controller) handleUpdateCnp(changed *ClusterNetworkPolicyChangedDelta) error {
	c.cnpKeyMutex.LockKey(changed.key)
	defer func() { _ = c.cnpKeyMutex.UnlockKey(changed.key) }()

	klog.Infof("handleUpdateCnp: processing CNP %s, field=%s, DNSReconcileDone=%v",
		changed.key, changed.field, changed.DNSReconcileDone)

	cachedCnp, err := c.cnpsLister.Get(changed.key)
	if err != nil {
		if k8serrors.IsNotFound(err) {
			return nil
		}
		klog.Error(err)
		return err
	}
	desiredCnp := cachedCnp.DeepCopy()
	klog.Infof("handle update cluster network policy %s", desiredCnp.Name)

	c.priorityMapMutex.RLock()
	err = c.validateCnpConfig(desiredCnp)
	c.priorityMapMutex.RUnlock()
	if err != nil {
		klog.Errorf("failed to validate cnp %s: %v", desiredCnp.Name, err)
		return err
	}

	cnpName := getCnpName(desiredCnp.Name)
	pgName := getCnpPortGroupName(desiredCnp)

	if changed.field == ChangedSubject {
		if err := c.setupCnpPortGroup(desiredCnp); err != nil {
			klog.Errorf("failed to create port group for cnp %s: %v", desiredCnp.Name, err)
			return err
		}
	}

	if changed.field == ChangedIngressRule {
		for index, rule := range desiredCnp.Spec.Ingress {
			if rule.Name == changed.ruleNames[index].curRuleName {
				if err := c.setAddrSetForCnpRule(cnpName, pgName, rule.Name, index, rule.From, []v1alpha2.ClusterNetworkPolicyEgressPeer{}, true); err != nil {
					klog.Errorf("failed to set ingress address-set for cnp rule %s/%s, %v", cnpName, rule.Name, err)
					return err
				}
			}
		}
	}

	if changed.field == ChangedEgressRule {
		for index, rule := range desiredCnp.Spec.Egress {
			needAddrSetUpdate := rule.Name == changed.ruleNames[index].curRuleName || changed.DNSReconcileDone

			needDNSReconcile := !changed.DNSReconcileDone

			if needAddrSetUpdate {
				if err := c.setAddrSetForCnpRule(cnpName, pgName, rule.Name, index, []v1alpha2.ClusterNetworkPolicyIngressPeer{}, rule.To, false); err != nil {
					klog.Errorf("failed to set egress address-set for cnp rule %s/%s, %v", cnpName, rule.Name, err)
					return err
				}

				if needDNSReconcile {
					c.domainResolver.setPolicyDomains(cnpName, getCnpDomainsNames(desiredCnp))
				}
			}
		}
	}

	return nil
}

func (c *Controller) handleDeleteCnp(cnp *v1alpha2.ClusterNetworkPolicy) error {
	c.cnpKeyMutex.LockKey(cnp.Name)
	defer func() { _ = c.cnpKeyMutex.UnlockKey(cnp.Name) }()

	klog.Infof("handle delete cluster network policy %s", cnp.Name)

	c.priorityMapMutex.Lock()
	err := c.deleteCnpPriorityMapEntries(cnp)
	c.priorityMapMutex.Unlock()
	if err != nil {
		klog.Errorf("failed to delete priorityMapEntries: %v", err)
	}

	cnpName := getCnpName(cnp.Name)

	pgName := getCnpPortGroupName(cnp)
	if err := c.OVNNbClient.DeletePortGroup(pgName); err != nil {
		klog.Errorf("failed to delete port group for cnp %s: %v", cnp.Name, err)
	}

	if err := c.OVNNbClient.DeleteAddressSets(map[string]string{
		clusterNetworkPolicyKey: fmt.Sprintf("%s/%s", cnpName, "ingress"),
	}); err != nil {
		klog.Errorf("failed to delete ingress address set for cnp %s: %v", cnp.Name, err)
	}

	if err := c.OVNNbClient.DeleteAddressSets(map[string]string{
		clusterNetworkPolicyKey: fmt.Sprintf("%s/%s", cnpName, "egress"),
	}); err != nil {
		klog.Errorf("failed to delete egress address set for cnp %s: %v", cnp.Name, err)
	}

	if _, err := c.cnpsLister.Get(cnpName); err != nil {
		c.domainResolver.setPolicyDomains(cnpName, nil)
	}

	return nil
}

func (c *Controller) getCnpCurrentAddrSetByName(cnpName string) (*strset.Set, *strset.Set, error) {
	curIngressAddrSet := strset.New()
	curEgressAddrSet := strset.New()

	operations := []string{"ingress", "egress"}
	for _, operation := range operations {
		addressSets, err := c.OVNNbClient.ListAddressSets(map[string]string{
			clusterNetworkPolicyKey: fmt.Sprintf("%s/%s", cnpName, operation),
		})
		if err != nil {
			klog.Errorf("failed to list %s address sets for cnp %s: %v", operation, cnpName, err)
			return nil, nil, err
		}

		for _, addressSet := range addressSets {
			if operation == "ingress" {
				curIngressAddrSet.Add(addressSet.Name)
				continue
			}

			curEgressAddrSet.Add(addressSet.Name)
		}
	}

	return curIngressAddrSet, curEgressAddrSet, nil
}

func (c *Controller) setupCnpPortGroup(cnp *v1alpha2.ClusterNetworkPolicy) error {
	pgName := getCnpPortGroupName(cnp)

	if err := c.OVNNbClient.CreatePortGroup(pgName, map[string]string{clusterNetworkPolicyKey: pgName}); err != nil {
		klog.Errorf("failed to create port group for cnp %s: %v", cnp.Name, err)
		return err
	}

	ports, err := c.getCnpPorts(&cnp.Spec.Subject)
	if err != nil {
		klog.Errorf("failed to fetch ports belongs to cnp %s: %v", cnp.Name, err)
		return err
	}

	if err = c.OVNNbClient.PortGroupSetPorts(pgName, ports); err != nil {
		klog.Errorf("failed to set ports %v to port group %s: %v", ports, pgName, err)
		return err
	}

	return nil
}

func (c *Controller) getCnpPorts(cnpSubject *v1alpha2.ClusterNetworkPolicySubject) ([]string, error) {
	var ports []string

	if cnpSubject.Namespaces != nil {
		nsSelector, err := metav1.LabelSelectorAsSelector(cnpSubject.Namespaces)
		if err != nil {
			return nil, fmt.Errorf("error creating ns label selector, %w", err)
		}
		ports, _, _, err = c.fetchPods(nsSelector, labels.Everything())
		if err != nil {
			return nil, fmt.Errorf("failed to fetch pods, %w", err)
		}
	} else if cnpSubject.Pods != nil {
		nsSelector, err := metav1.LabelSelectorAsSelector(&cnpSubject.Pods.NamespaceSelector)
		if err != nil {
			return nil, fmt.Errorf("error creating ns label selector, %w", err)
		}
		podSelector, err := metav1.LabelSelectorAsSelector(&cnpSubject.Pods.PodSelector)
		if err != nil {
			return nil, fmt.Errorf("error creating pod label selector, %w", err)
		}
		ports, _, _, err = c.fetchPods(nsSelector, podSelector)
		if err != nil {
			return nil, fmt.Errorf("failed to fetch pods, %w", err)
		}
	}

	return ports, nil
}

func (c *Controller) generateCnpIngressAddressSet(cnpName, pgName string, rule v1alpha2.ClusterNetworkPolicyIngressRule, index int) (string, int, string, int, error) {
	ingressAsV4Name, ingressAsV6Name := getAnpAddressSetName(pgName, rule.Name, index, true)

	var v4Addrs, v6Addrs []string
	var err error

	for _, peer := range rule.From {
		var v4Addresses, v6Addresses []string
		if v4Addresses, v6Addresses, err = c.fetchIngressSelectedAddressesByCnp(&peer); err != nil {
			return "", 0, "", 0, err
		}
		v4Addrs = append(v4Addrs, v4Addresses...)
		v6Addrs = append(v6Addrs, v6Addresses...)
	}

	if err = c.createCnpAddressSet(cnpName, rule.Name, "ingress", ingressAsV4Name, v4Addrs); err != nil {
		klog.Error(err)
		return "", 0, "", 0, err
	}

	if err = c.createCnpAddressSet(cnpName, rule.Name, "ingress", ingressAsV6Name, v6Addrs); err != nil {
		klog.Error(err)
		return "", 0, "", 0, err
	}

	return ingressAsV4Name, len(v4Addrs), ingressAsV6Name, len(v6Addrs), nil
}

func (c *Controller) generateCnpEgressAddressSet(cnpName, pgName string, rule v1alpha2.ClusterNetworkPolicyEgressRule, index int) (string, int, string, int, error) {
	egressAsV4Name, egressAsV6Name := getAnpAddressSetName(pgName, rule.Name, index, false)

	var v4Addrs, v6Addrs []string
	var err error

	for _, peer := range rule.To {
		var v4Addresses, v6Addresses []string
		if v4Addresses, v6Addresses, err = c.fetchEgressSelectedAddressesByCnp(&peer); err != nil {
			return "", 0, "", 0, err
		}
		v4Addrs = append(v4Addrs, v4Addresses...)
		v6Addrs = append(v6Addrs, v6Addresses...)
	}

	if err = c.createCnpAddressSet(cnpName, rule.Name, "egress", egressAsV4Name, v4Addrs); err != nil {
		klog.Error(err)
		return "", 0, "", 0, err
	}

	if err = c.createCnpAddressSet(cnpName, rule.Name, "egress", egressAsV6Name, v6Addrs); err != nil {
		klog.Error(err)
		return "", 0, "", 0, err
	}

	return egressAsV4Name, len(v4Addrs), egressAsV6Name, len(v6Addrs), nil
}

func (c *Controller) createCnpAddressSet(cnpName, ruleName, direction, asName string, addresses []string) error {
	if err := c.OVNNbClient.CreateAddressSet(asName, map[string]string{
		clusterNetworkPolicyKey: fmt.Sprintf("%s/%s", cnpName, direction),
	}); err != nil {
		klog.Errorf("failed to create ovn address set %s for cnp rule %s/%s: %v", asName, cnpName, ruleName, err)
		return err
	}

	if err := c.OVNNbClient.AddressSetUpdateAddress(asName, addresses...); err != nil {
		klog.Errorf("failed to set addresses %q to address set %s: %v", strings.Join(addresses, ","), asName, err)
		return err
	}

	return nil
}

func (c *Controller) fetchIngressSelectedAddressesByCnp(ingressPeer *v1alpha2.ClusterNetworkPolicyIngressPeer) ([]string, []string, error) {
	var v4Addresses, v6Addresses []string

	if ingressPeer.Namespaces != nil {
		nsSelector, err := metav1.LabelSelectorAsSelector(ingressPeer.Namespaces)
		if err != nil {
			return nil, nil, fmt.Errorf("error creating ns label selector, %w", err)
		}
		_, v4Addresses, v6Addresses, err = c.fetchPods(nsSelector, labels.Everything())
		if err != nil {
			return nil, nil, fmt.Errorf("failed to fetch ingress peer addresses, %w", err)
		}
	} else if ingressPeer.Pods != nil {
		nsSelector, err := metav1.LabelSelectorAsSelector(&ingressPeer.Pods.NamespaceSelector)
		if err != nil {
			return nil, nil, fmt.Errorf("error creating ns label selector, %w", err)
		}
		podSelector, err := metav1.LabelSelectorAsSelector(&ingressPeer.Pods.PodSelector)
		if err != nil {
			return nil, nil, fmt.Errorf("error creating pod label selector, %w", err)
		}
		_, v4Addresses, v6Addresses, err = c.fetchPods(nsSelector, podSelector)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to fetch ingress peer addresses, %w", err)
		}
	}

	return v4Addresses, v6Addresses, nil
}

func (c *Controller) fetchEgressSelectedAddressesByCnp(egressPeer *v1alpha2.ClusterNetworkPolicyEgressPeer) ([]string, []string, error) {
	return c.fetchEgressSelectedAddressesCommonByCnp(egressPeer.Namespaces, egressPeer.Pods, egressPeer.Nodes, egressPeer.Networks, egressPeer.DomainNames)
}

func (c *Controller) fetchEgressSelectedAddressesCommonByCnp(namespaces *metav1.LabelSelector, pods *v1alpha2.NamespacedPod, nodes *metav1.LabelSelector, networks []v1alpha2.CIDR, domainNames []v1alpha2.DomainName) ([]string, []string, error) {
	var v4Addresses, v6Addresses []string

	switch {
	case namespaces != nil:
		nsSelector, err := metav1.LabelSelectorAsSelector(namespaces)
		if err != nil {
			return nil, nil, fmt.Errorf("error creating ns label selector, %w", err)
		}

		_, v4Addresses, v6Addresses, err = c.fetchPods(nsSelector, labels.Everything())
		if err != nil {
			return nil, nil, fmt.Errorf("failed to fetch egress peer addresses, %w", err)
		}
	case pods != nil:
		nsSelector, err := metav1.LabelSelectorAsSelector(&pods.NamespaceSelector)
		if err != nil {
			return nil, nil, fmt.Errorf("error creating ns label selector, %w", err)
		}
		podSelector, err := metav1.LabelSelectorAsSelector(&pods.PodSelector)
		if err != nil {
			return nil, nil, fmt.Errorf("error creating pod label selector, %w", err)
		}

		_, v4Addresses, v6Addresses, err = c.fetchPods(nsSelector, podSelector)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to fetch egress peer addresses, %w", err)
		}
	case nodes != nil:
		nodesSelector, err := metav1.LabelSelectorAsSelector(nodes)
		if err != nil {
			return nil, nil, fmt.Errorf("error creating nodes label selector, %w", err)
		}
		v4Addresses, v6Addresses, err = c.fetchNodesAddrs(nodesSelector)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to fetch egress peer addresses, %w", err)
		}
	case len(networks) != 0:
		v4Addresses, v6Addresses = fetchCnpCIDRAddresses(networks)
	case len(domainNames) != 0:
		klog.Infof("DomainNames detected in egress peer: %v", domainNames)
		v4Addresses, v6Addresses = c.resolveDomainNamesForCnp(domainNames)
	default:
		return nil, nil, errors.New("at least one egressPeer must be specified")
	}

	return v4Addresses, v6Addresses, nil
}

func (c *Controller) setAddrSetForCnpRule(anpName, pgName, ruleName string, index int, from []v1alpha2.ClusterNetworkPolicyIngressPeer, to []v1alpha2.ClusterNetworkPolicyEgressPeer, isIngress bool) error {
	var v4Addrs, v4Addr, v6Addrs, v6Addr []string
	var err error
	if isIngress {
		for _, anprpeer := range from {
			if v4Addr, v6Addr, err = c.fetchIngressSelectedAddressesByCnp(&anprpeer); err != nil {
				klog.Errorf("failed to fetch anp/banp ingress selected addresses, %v", err)
				return err
			}
			v4Addrs = append(v4Addrs, v4Addr...)
			v6Addrs = append(v6Addrs, v6Addr...)
		}
		klog.Infof("update anp/banp ingress rule %s, selected v4 address %v, v6 address %v", ruleName, v4Addrs, v6Addrs)

		gressAsV4Name, gressAsV6Name := getAnpAddressSetName(pgName, ruleName, index, true)
		if err = c.createCnpAddressSet(anpName, ruleName, "ingress", gressAsV4Name, v4Addrs); err != nil {
			klog.Error(err)
			return err
		}
		if err = c.createCnpAddressSet(anpName, ruleName, "ingress", gressAsV6Name, v6Addrs); err != nil {
			klog.Error(err)
			return err
		}
	} else {
		for _, anprpeer := range to {
			if v4Addr, v6Addr, err = c.fetchEgressSelectedAddressesByCnp(&anprpeer); err != nil {
				klog.Errorf("failed to fetch anp/banp egress selected addresses, %v", err)
				return err
			}
			v4Addrs = append(v4Addrs, v4Addr...)
			v6Addrs = append(v6Addrs, v6Addr...)
		}
		klog.Infof("update anp/banp egress rule %s, selected v4 address %v, v6 address %v", ruleName, v4Addrs, v6Addrs)

		gressAsV4Name, gressAsV6Name := getAnpAddressSetName(pgName, ruleName, index, false)
		if err = c.createCnpAddressSet(anpName, ruleName, "egress", gressAsV4Name, v4Addrs); err != nil {
			klog.Error(err)
			return err
		}
		if err = c.createCnpAddressSet(anpName, ruleName, "egress", gressAsV6Name, v6Addrs); err != nil {
			klog.Error(err)
			return err
		}
	}

	return nil
}

func (c *Controller) resolveDomainNamesForCnp(domainNames []v1alpha2.DomainName) ([]string, []string) {
	var allV4Addresses, allV6Addresses []string
	for _, domainName := range domainNames {
		v4Addresses, v6Addresses := c.domainResolver.addresses(string(domainName))
		allV4Addresses = append(allV4Addresses, v4Addresses...)
		allV6Addresses = append(allV6Addresses, v6Addresses...)
	}
	return allV4Addresses, allV6Addresses
}

func (c *Controller) updateCnpsByLabelsMatch(nsLabels, podLabels map[string]string) {
	cnps, _ := c.cnpsLister.List(labels.Everything())
	for _, cnp := range cnps {
		changed := &ClusterNetworkPolicyChangedDelta{
			key: cnp.Name,
		}

		if doCnpLabelsMatch(cnp.Spec.Subject.Namespaces, cnp.Spec.Subject.Pods, nsLabels, podLabels) {
			klog.Infof("cnp %s, labels matched for cnp's subject, nsLabels %s, podLabels %s", cnp.Name, labels.Set(nsLabels).String(), labels.Set(podLabels).String())
			changed.field = ChangedSubject
			c.updateCnpQueue.Add(changed)
		}

		ingressRuleNames, egressRuleNames := getAffectedCnpRules(cnp, nsLabels, podLabels)
		if !isCnpRulesArrayEmpty(ingressRuleNames) {
			klog.Infof("cnp %s, labels matched for cnp's ingress peer, nsLabels %s, podLabels %s", cnp.Name, labels.Set(nsLabels).String(), labels.Set(podLabels).String())
			changed.ruleNames = ingressRuleNames
			changed.field = ChangedIngressRule
			c.updateCnpQueue.Add(changed)
		}

		if !isCnpRulesArrayEmpty(egressRuleNames) {
			klog.Infof("cnp %s, labels matched for cnp's egress peer, nsLabels %s, podLabels %s", cnp.Name, labels.Set(nsLabels).String(), labels.Set(podLabels).String())
			changed.ruleNames = egressRuleNames
			changed.field = ChangedEgressRule
			c.updateCnpQueue.Add(changed)
		}
	}
}

func getAffectedCnpRules(cnp *v1alpha2.ClusterNetworkPolicy, nsLabels, podLabels map[string]string) ([util.CnpMaxRules]ChangedName, [util.CnpMaxRules]ChangedName) {
	var changedIngressRuleNames, changedEgressRuleNames [util.CnpMaxRules]ChangedName

	for index, rule := range cnp.Spec.Ingress {
		for _, from := range rule.From {
			if doCnpLabelsMatch(from.Namespaces, from.Pods, nsLabels, podLabels) {
				changedIngressRuleNames[index].curRuleName = rule.Name
			}
		}
	}

	for index, rule := range cnp.Spec.Egress {
		for _, to := range rule.To {
			if doCnpLabelsMatch(to.Namespaces, to.Pods, nsLabels, podLabels) {
				changedEgressRuleNames[index].curRuleName = rule.Name
			}
		}
	}

	return changedIngressRuleNames, changedEgressRuleNames
}

func isCnpRulesArrayEmpty(rules [util.CnpMaxRules]ChangedName) bool {
	for _, rule := range rules {
		if rule.curRuleName != "" {
			return false
		}
	}
	return true
}

func getCnpPortGroupName(cnp *v1alpha2.ClusterNetworkPolicy) string {
	return strings.ReplaceAll(getCnpName(cnp.Name), "-", ".")
}

func shouldUpdateCnpPortGroup(oldCnp, newCnp *v1alpha2.ClusterNetworkPolicy) bool {
	return !reflect.DeepEqual(oldCnp.Spec.Subject, newCnp.Spec.Subject)
}

func getCnpAddressSetsToUpdate(oldCnp, newCnp *v1alpha2.ClusterNetworkPolicy) (ingress, egress [util.CnpMaxRules]ChangedName) {
	for index, rule := range newCnp.Spec.Ingress {
		oldRule := oldCnp.Spec.Ingress[index]
		change := ChangedName{}

		if !reflect.DeepEqual(oldRule.From, rule.From) {
			change.curRuleName = rule.Name
		}

		ingress[index] = change
	}

	for index, rule := range newCnp.Spec.Egress {
		oldRule := oldCnp.Spec.Egress[index]
		change := ChangedName{}

		if !reflect.DeepEqual(oldRule.To, rule.To) {
			change.curRuleName = rule.Name
		}

		egress[index] = change
	}

	return ingress, egress
}

func shouldRecreateCnpACLs(oldCnp, newCnp *v1alpha2.ClusterNetworkPolicy) bool {
	tierChanged := oldCnp.Spec.Tier != newCnp.Spec.Tier
	priorityChanged := oldCnp.Spec.Priority != newCnp.Spec.Priority
	ingressCountChanged := len(oldCnp.Spec.Ingress) != len(newCnp.Spec.Ingress)
	egressCountChanged := len(oldCnp.Spec.Egress) != len(newCnp.Spec.Egress)
	logChanged := oldCnp.Annotations[util.ACLActionsLogAnnotation] != newCnp.Annotations[util.ACLActionsLogAnnotation]

	if tierChanged || priorityChanged || ingressCountChanged || egressCountChanged || logChanged {
		return true
	}

	for index, rule := range newCnp.Spec.Ingress {
		oldRule := oldCnp.Spec.Ingress[index]
		if oldRule.Name != rule.Name || oldRule.Action != rule.Action || !reflect.DeepEqual(oldRule.Ports, rule.Ports) {
			return true
		}
	}

	for index, rule := range newCnp.Spec.Egress {
		oldRule := oldCnp.Spec.Egress[index]
		if oldRule.Name != rule.Name || oldRule.Action != rule.Action || !reflect.DeepEqual(oldRule.Ports, rule.Ports) {
			return true
		}
	}

	return false
}

func (c *Controller) getCnpPriorityMaps(tier v1alpha2.Tier) (map[int32]string, map[string]int32, error) {
	switch tier {
	case v1alpha2.AdminTier:
		return c.anpPrioNameMap, c.anpNamePrioMap, nil
	case v1alpha2.BaselineTier:
		return c.bnpPrioNameMap, c.bnpNamePrioMap, nil
	default:
		return nil, nil, fmt.Errorf("unknown cnp tier %s", tier)
	}
}

func (c *Controller) updateCnpPriorityMapEntries(cnp *v1alpha2.ClusterNetworkPolicy) error {
	if err := c.wipeCnpPriorityMapEntries(cnp); err != nil {
		return fmt.Errorf("failed to handle tier change for cnp %s: %w", cnp.Name, err)
	}

	priorityNameMap, namePriorityMap, err := c.getCnpPriorityMaps(cnp.Spec.Tier)
	if err != nil {
		return fmt.Errorf("failed to get priority maps for cnp %s: %w", cnp.Name, err)
	}

	priorityNameMap[cnp.Spec.Priority] = cnp.Name
	namePriorityMap[cnp.Name] = cnp.Spec.Priority

	return nil
}

func (c *Controller) deleteCnpPriorityMapEntries(cnp *v1alpha2.ClusterNetworkPolicy) error {
	priorityNameMap, namePriorityMap, err := c.getCnpPriorityMaps(cnp.Spec.Tier)
	if err != nil {
		return fmt.Errorf("failed to get priority maps for cnp %s: %w", cnp.Name, err)
	}

	delete(priorityNameMap, cnp.Spec.Priority)
	delete(namePriorityMap, cnp.Name)

	return nil
}

func (c *Controller) wipeCnpPriorityMapEntries(cnp *v1alpha2.ClusterNetworkPolicy) error {
	tiers := []v1alpha2.Tier{v1alpha2.AdminTier, v1alpha2.BaselineTier}

	for _, tier := range tiers {
		priorityNameMap, namePriorityMap, err := c.getCnpPriorityMaps(tier)
		if err != nil {
			return fmt.Errorf("failed to get priority maps for cnp %s: %w", cnp.Name, err)
		}

		if priority, ok := namePriorityMap[cnp.Name]; ok {
			delete(priorityNameMap, priority)
			delete(namePriorityMap, cnp.Name)
		}
	}

	return nil
}

func (c *Controller) validateCnpConfig(cnp *v1alpha2.ClusterNetworkPolicy) error {
	priorityNameMap, _, err := c.getCnpPriorityMaps(cnp.Spec.Tier)
	if err != nil {
		err := fmt.Errorf("failed to get priority maps for cnp %s: %w", cnp.Name, err)
		klog.Error(err)
		return err
	}

	if err := checkCnpPriorities(priorityNameMap, cnp); err != nil {
		return err
	}

	if len(cnp.Spec.Ingress) > util.CnpMaxRules || len(cnp.Spec.Egress) > util.CnpMaxRules {
		err := fmt.Errorf("at most %d rules allowed by ingress/egress section for cnp %s, got %d ingress rules and %d egress rules", util.CnpMaxRules, cnp.Name, len(cnp.Spec.Ingress), len(cnp.Spec.Egress))
		klog.Error(err)
		return err
	}

	if err := checkNetworkAndDomainRules(cnp); err != nil {
		return err
	}

	return nil
}

func checkCnpPriorities(priorityNameMap map[int32]string, cnp *v1alpha2.ClusterNetworkPolicy) error {
	if priorityNameMap == nil || cnp == nil {
		err := errors.New("must provide a priorityMap and a CNP")
		klog.Error(err)
		return err
	}

	if cnpName, exist := priorityNameMap[cnp.Spec.Priority]; exist && cnpName != cnp.Name {
		err := fmt.Errorf("can not create cnp %s with priority %d, cnp %s already exists with the same priority", cnp.Name, cnp.Spec.Priority, cnpName)
		klog.Error(err)
		return err
	}

	// We have noticed RedHat's discussion about ACL priority in https://bugzilla.redhat.com/show_bug.cgi?id=2175752
	// After discussion, we decided to use the same range of priorities (20000-30000). Pay tribute to the developers of RedHat.
	// This is a deviation from the standard of the API (max priority should be 1000).
	if cnp.Spec.Priority > util.CnpMaxPriority || cnp.Spec.Priority < 0 {
		err := fmt.Errorf("priority of cnp %s is not within bounds 0 to %d", cnp.Name, util.CnpMaxPriority)
		klog.Error(err)
		return err
	}

	return nil
}

func checkNetworkAndDomainRules(cnp *v1alpha2.ClusterNetworkPolicy) error {
	for _, egressRule := range cnp.Spec.Egress {
		for _, peer := range egressRule.To {
			if len(peer.DomainNames) > util.CnpMaxDomains {
				return fmt.Errorf("cnp egress peers can have a maximum of %d domains, got %d", util.CnpMaxDomains, len(peer.DomainNames))
			}

			if len(peer.Networks) > util.CnpMaxNetworks {
				return fmt.Errorf("cnp egress peers can have a maximum of %d domains, got %d", util.CnpMaxNetworks, len(peer.Networks))
			}
		}
	}

	return nil
}

func fetchCnpCIDRAddresses(networks []v1alpha2.CIDR) ([]string, []string) {
	var v4Addresses, v6Addresses []string

	for _, network := range networks {
		if _, _, err := net.ParseCIDR(string(network)); err != nil {
			klog.Errorf("invalid cidr %s", string(network))
			continue
		}
		switch util.CheckProtocol(string(network)) {
		case fabricv1.ProtocolIPv4:
			v4Addresses = append(v4Addresses, string(network))
		case fabricv1.ProtocolIPv6:
			v6Addresses = append(v6Addresses, string(network))
		}
	}

	return v4Addresses, v6Addresses
}

func getCnpName(name string) string {
	nameArray := []rune(name)

	if !unicode.IsLetter(nameArray[0]) {
		name = clusterNetworkPolicyKey + name
	}

	return name
}

func getCnpACLAction(action v1alpha2.ClusterNetworkPolicyRuleAction) ovnnb.ACLAction {
	switch action {
	case v1alpha2.ClusterNetworkPolicyRuleActionAccept:
		return ovnnb.ACLActionAllowRelated
	case v1alpha2.ClusterNetworkPolicyRuleActionDeny:
		return ovnnb.ACLActionDrop
	case v1alpha2.ClusterNetworkPolicyRuleActionPass:
		return ovnnb.ACLActionPass
	default:
		return ovnnb.ACLActionDrop
	}
}

func getCnpACLTier(tier v1alpha2.Tier) int {
	switch tier {
	case v1alpha2.AdminTier:
		return util.AnpACLTier
	case v1alpha2.BaselineTier:
		return util.BanpACLTier
	default:
		return util.BanpACLTier
	}
}

func getCnpDomainsNames(cnp *v1alpha2.ClusterNetworkPolicy) (domainNames []string) {
	for _, rule := range cnp.Spec.Egress {
		for _, to := range rule.To {
			for _, domainName := range to.DomainNames {
				domainNames = append(domainNames, string(domainName))
			}
		}
	}

	return domainNames
}

func hasCnpDomainNames(cnp *v1alpha2.ClusterNetworkPolicy) bool {
	for _, rule := range cnp.Spec.Egress {
		for _, to := range rule.To {
			if len(to.DomainNames) > 0 {
				return true
			}
		}
	}

	return false
}

func getCnpACLPriority(cnp *v1alpha2.ClusterNetworkPolicy, index int) int {
	return util.CnpACLMaxPriority - int(cnp.Spec.Priority*util.CnpMaxRules) - index
}

func getCnpACLName(cnpName, protocol, direction string, index int) string {
	return fmt.Sprintf("%s/%s/%s/%s/%d", clusterNetworkPolicyKey, cnpName, direction, protocol, index)
}

func doCnpLabelsMatch(namespaces *metav1.LabelSelector, pods *v1alpha2.NamespacedPod, nsLabels, podLabels map[string]string) bool {
	if namespaces != nil {
		nsSelector, _ := metav1.LabelSelectorAsSelector(namespaces)
		if nsSelector.Matches(labels.Set(nsLabels)) {
			return true
		}
	} else if pods != nil {
		nsSelector, _ := metav1.LabelSelectorAsSelector(&pods.NamespaceSelector)
		podSelector, _ := metav1.LabelSelectorAsSelector(&pods.PodSelector)
		if nsSelector.Matches(labels.Set(nsLabels)) && podSelector.Matches(labels.Set(podLabels)) {
			return true
		}
	}

	return false
}
