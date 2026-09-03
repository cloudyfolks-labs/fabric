package controller

import (
	"context"
	"fmt"

	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"

	kubeovnv1 "github.com/cloudyfolks-labs/fabric/pkg/apis/kubeovn/v1"
	"github.com/cloudyfolks-labs/fabric/pkg/util"
)

// The LoadBalancer CR is the one public L4 balancer object: its
// frontend field picks the OVN realization. The controller translates
// it into an owned SwitchLBRule (literal vip, switch-attached) or
// RouterLBRule (ovnEip, router-attached) child, so the two engines
// stay single-purpose while the API stops carrying two shapes.

func lbChildName(name string) string {
	return "flb-" + name
}

func (c *Controller) enqueueAddLoadBalancer(obj any) {
	key := cache.MetaObjectToName(obj.(*kubeovnv1.LoadBalancer)).String()
	klog.Infof("enqueue add LoadBalancer %s", key)
	c.addLoadBalancerQueue.Add(key)
}

func (c *Controller) enqueueUpdateLoadBalancer(oldObj, newObj any) {
	oldLb := oldObj.(*kubeovnv1.LoadBalancer)
	newLb := newObj.(*kubeovnv1.LoadBalancer)
	if oldLb.ResourceVersion == newLb.ResourceVersion {
		return
	}
	c.addLoadBalancerQueue.Add(newLb.Name)
}

func (c *Controller) enqueueDeleteLoadBalancer(obj any) {
	var lb *kubeovnv1.LoadBalancer
	switch t := obj.(type) {
	case *kubeovnv1.LoadBalancer:
		lb = t
	case cache.DeletedFinalStateUnknown:
		l, ok := t.Obj.(*kubeovnv1.LoadBalancer)
		if !ok {
			klog.Warningf("unexpected object type: %T", t.Obj)
			return
		}
		lb = l
	default:
		klog.Warningf("unexpected type: %T", obj)
		return
	}
	klog.Infof("enqueue del LoadBalancer %s", lb.Name)
	c.delLoadBalancerQueue.Add(lb.Name)
}

// enqueueLoadBalancerForChild re-reconciles the owner when a child
// rule's status moves, so the LoadBalancer status follows.
func (c *Controller) enqueueLoadBalancerForChild(obj any) {
	if unknown, ok := obj.(cache.DeletedFinalStateUnknown); ok {
		obj = unknown.Obj
	}
	accessor, err := meta.Accessor(obj)
	if err != nil {
		return
	}
	if owner := accessor.GetLabels()[util.LoadBalancerOwnerLabel]; owner != "" {
		c.addLoadBalancerQueue.Add(owner)
	}
}

func (c *Controller) handleAddOrUpdateLoadBalancer(name string) error {
	klog.V(3).Infof("handleAddOrUpdateLoadBalancer %s", name)
	lb, err := c.loadBalancerLister.Get(name)
	if err != nil {
		if k8serrors.IsNotFound(err) {
			return nil
		}
		klog.Error(err)
		return err
	}

	hasVip, hasEip := lb.Spec.Frontend.Vip != "", lb.Spec.Frontend.OvnEip != ""
	if hasVip == hasEip {
		err := fmt.Errorf("LoadBalancer %s: exactly one of frontend.vip and frontend.ovnEip must be set", name)
		klog.Error(err)
		return c.patchLoadBalancerStatus(lb, "", "ValidateFailed", err.Error())
	}

	child := lbChildName(lb.Name)
	childLabels := map[string]string{util.LoadBalancerOwnerLabel: lb.Name}

	if hasVip {
		if err := c.deleteLoadBalancerChildRouterRule(child); err != nil {
			return err
		}
		vip := lb.Spec.Frontend.Vip
		// No network annotations: the endpoint funnel resolves the VPC
		// and the backends' subnet from the endpoint targets, which is
		// exactly the placement the old annotation contract asked the
		// caller to know.
		desired := &kubeovnv1.SwitchLBRule{
			ObjectMeta: metav1.ObjectMeta{
				Name:   child,
				Labels: childLabels,
			},
			Spec: kubeovnv1.SwitchLBRuleSpec{
				Vip:             vip,
				Namespace:       lb.Spec.Namespace,
				Selector:        lb.Spec.Selector,
				Endpoints:       lb.Spec.Endpoints,
				SessionAffinity: lb.Spec.SessionAffinity,
				Ports:           toSwitchLBRulePorts(lb.Spec.Ports),
			},
		}
		if err := c.upsertLoadBalancerChildSwitchRule(desired); err != nil {
			return err
		}
		return c.syncLoadBalancerStatusFromSwitchRule(lb, child, vip)
	}

	if err := c.deleteLoadBalancerChildSwitchRule(child); err != nil {
		return err
	}
	desired := &kubeovnv1.RouterLBRule{
		ObjectMeta: metav1.ObjectMeta{
			Name:   child,
			Labels: childLabels,
		},
		Spec: kubeovnv1.RouterLBRuleSpec{
			OvnEip:          lb.Spec.Frontend.OvnEip,
			Vpc:             lb.Spec.Vpc,
			Namespace:       lb.Spec.Namespace,
			Selector:        lb.Spec.Selector,
			Endpoints:       lb.Spec.Endpoints,
			SessionAffinity: lb.Spec.SessionAffinity,
			Ports:           toRouterLBRulePorts(lb.Spec.Ports),
		},
	}
	if err := c.upsertLoadBalancerChildRouterRule(desired); err != nil {
		return err
	}
	return c.syncLoadBalancerStatusFromRouterRule(lb, child)
}

func (c *Controller) syncLoadBalancerStatusFromRouterRule(lb *kubeovnv1.LoadBalancer, child string) error {
	vip := ""
	if eip, err := c.ovnEipsLister.Get(lb.Spec.Frontend.OvnEip); err == nil {
		vip = eip.Status.V4Ip
	}
	rule, err := c.routerLBRuleLister.Get(child)
	if err != nil {
		if k8serrors.IsNotFound(err) {
			return c.patchLoadBalancerStatus(lb, vip, "Translating", "child rule not observed yet")
		}
		return err
	}
	updated := lb.DeepCopy()
	updated.Status.Vip = vip
	updated.Status.Ports = rule.Status.Ports
	updated.Status.Service = rule.Status.Service
	_, err = c.config.KubeOvnClient.FabricV1().LoadBalancers().UpdateStatus(context.Background(), updated, metav1.UpdateOptions{})
	return err
}

func (c *Controller) handleDelLoadBalancer(name string) error {
	klog.V(3).Infof("handleDelLoadBalancer %s", name)
	child := lbChildName(name)
	if err := c.deleteLoadBalancerChildSwitchRule(child); err != nil {
		return err
	}
	return c.deleteLoadBalancerChildRouterRule(child)
}

func (c *Controller) upsertLoadBalancerChildSwitchRule(desired *kubeovnv1.SwitchLBRule) error {
	existing, err := c.switchLBRuleLister.Get(desired.Name)
	if k8serrors.IsNotFound(err) {
		_, err = c.config.KubeOvnClient.FabricV1().SwitchLBRules().Create(context.Background(), desired, metav1.CreateOptions{})
		return err
	}
	if err != nil {
		return err
	}
	updated := existing.DeepCopy()
	updated.Labels = desired.Labels
	updated.Annotations = desired.Annotations
	updated.Spec = desired.Spec
	_, err = c.config.KubeOvnClient.FabricV1().SwitchLBRules().Update(context.Background(), updated, metav1.UpdateOptions{})
	return err
}

func (c *Controller) upsertLoadBalancerChildRouterRule(desired *kubeovnv1.RouterLBRule) error {
	existing, err := c.routerLBRuleLister.Get(desired.Name)
	if k8serrors.IsNotFound(err) {
		_, err = c.config.KubeOvnClient.FabricV1().RouterLBRules().Create(context.Background(), desired, metav1.CreateOptions{})
		return err
	}
	if err != nil {
		return err
	}
	updated := existing.DeepCopy()
	updated.Labels = desired.Labels
	updated.Spec = desired.Spec
	_, err = c.config.KubeOvnClient.FabricV1().RouterLBRules().Update(context.Background(), updated, metav1.UpdateOptions{})
	return err
}

func (c *Controller) deleteLoadBalancerChildSwitchRule(name string) error {
	if err := c.config.KubeOvnClient.FabricV1().SwitchLBRules().Delete(context.Background(), name, metav1.DeleteOptions{}); err != nil && !k8serrors.IsNotFound(err) {
		return err
	}
	return nil
}

func (c *Controller) deleteLoadBalancerChildRouterRule(name string) error {
	if err := c.config.KubeOvnClient.FabricV1().RouterLBRules().Delete(context.Background(), name, metav1.DeleteOptions{}); err != nil && !k8serrors.IsNotFound(err) {
		return err
	}
	return nil
}

func (c *Controller) syncLoadBalancerStatusFromSwitchRule(lb *kubeovnv1.LoadBalancer, child, vip string) error {
	rule, err := c.switchLBRuleLister.Get(child)
	if err != nil {
		if k8serrors.IsNotFound(err) {
			return c.patchLoadBalancerStatus(lb, vip, "Translating", "child rule not observed yet")
		}
		return err
	}
	updated := lb.DeepCopy()
	updated.Status.Vip = vip
	updated.Status.Ports = rule.Status.Ports
	updated.Status.Service = rule.Status.Service
	_, err = c.config.KubeOvnClient.FabricV1().LoadBalancers().UpdateStatus(context.Background(), updated, metav1.UpdateOptions{})
	return err
}

func (c *Controller) patchLoadBalancerStatus(lb *kubeovnv1.LoadBalancer, vip, reason, message string) error {
	updated := lb.DeepCopy()
	updated.Status.Vip = vip
	if _, err := c.config.KubeOvnClient.FabricV1().LoadBalancers().UpdateStatus(context.Background(), updated, metav1.UpdateOptions{}); err != nil {
		klog.Errorf("update LoadBalancer %s status: %v", lb.Name, err)
		return err
	}
	if message != "" {
		klog.V(3).Infof("LoadBalancer %s: %s: %s", lb.Name, reason, message)
	}
	return nil
}

func toSwitchLBRulePorts(ports []kubeovnv1.LoadBalancerPort) []kubeovnv1.SwitchLBRulePort {
	out := make([]kubeovnv1.SwitchLBRulePort, 0, len(ports))
	for _, p := range ports {
		out = append(out, kubeovnv1.SwitchLBRulePort(p))
	}
	return out
}

func toRouterLBRulePorts(ports []kubeovnv1.LoadBalancerPort) []kubeovnv1.RouterLBRulePort {
	out := make([]kubeovnv1.RouterLBRulePort, 0, len(ports))
	for _, p := range ports {
		out = append(out, kubeovnv1.RouterLBRulePort(p))
	}
	return out
}
