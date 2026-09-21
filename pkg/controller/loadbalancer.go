package controller

import (
	"context"
	"fmt"

	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"

	fabricv1 "github.com/cloudyfolks-labs/fabric/pkg/apis/fabric/v1"
	"github.com/cloudyfolks-labs/fabric/pkg/util"
)

func lbChildName(name string) string {
	return "flb-" + name
}

func (c *Controller) enqueueAddLoadBalancer(obj any) {
	key := cache.MetaObjectToName(obj.(*fabricv1.LoadBalancer)).String()
	klog.Infof("enqueue add LoadBalancer %s", key)
	c.addLoadBalancerQueue.Add(key)
}

func (c *Controller) enqueueUpdateLoadBalancer(oldObj, newObj any) {
	oldLb := oldObj.(*fabricv1.LoadBalancer)
	newLb := newObj.(*fabricv1.LoadBalancer)
	if oldLb.ResourceVersion == newLb.ResourceVersion {
		return
	}
	c.addLoadBalancerQueue.Add(newLb.Name)
}

func (c *Controller) enqueueDeleteLoadBalancer(obj any) {
	var lb *fabricv1.LoadBalancer
	switch t := obj.(type) {
	case *fabricv1.LoadBalancer:
		lb = t
	case cache.DeletedFinalStateUnknown:
		l, ok := t.Obj.(*fabricv1.LoadBalancer)
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

		desired := &fabricv1.SwitchLBRule{
			ObjectMeta: metav1.ObjectMeta{
				Name:   child,
				Labels: childLabels,
			},
			Spec: fabricv1.SwitchLBRuleSpec{
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
	desired := &fabricv1.RouterLBRule{
		ObjectMeta: metav1.ObjectMeta{
			Name:   child,
			Labels: childLabels,
		},
		Spec: fabricv1.RouterLBRuleSpec{
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

func (c *Controller) syncLoadBalancerStatusFromRouterRule(lb *fabricv1.LoadBalancer, child string) error {
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
	_, err = c.config.FabricClient.FabricV1().LoadBalancers().UpdateStatus(context.Background(), updated, metav1.UpdateOptions{})
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

func (c *Controller) upsertLoadBalancerChildSwitchRule(desired *fabricv1.SwitchLBRule) error {
	existing, err := c.switchLBRuleLister.Get(desired.Name)
	if k8serrors.IsNotFound(err) {
		_, err = c.config.FabricClient.FabricV1().SwitchLBRules().Create(context.Background(), desired, metav1.CreateOptions{})
		return err
	}
	if err != nil {
		return err
	}
	updated := existing.DeepCopy()
	updated.Labels = desired.Labels
	updated.Annotations = desired.Annotations
	updated.Spec = desired.Spec
	_, err = c.config.FabricClient.FabricV1().SwitchLBRules().Update(context.Background(), updated, metav1.UpdateOptions{})
	return err
}

func (c *Controller) upsertLoadBalancerChildRouterRule(desired *fabricv1.RouterLBRule) error {
	existing, err := c.routerLBRuleLister.Get(desired.Name)
	if k8serrors.IsNotFound(err) {
		_, err = c.config.FabricClient.FabricV1().RouterLBRules().Create(context.Background(), desired, metav1.CreateOptions{})
		return err
	}
	if err != nil {
		return err
	}
	updated := existing.DeepCopy()
	updated.Labels = desired.Labels
	updated.Spec = desired.Spec
	_, err = c.config.FabricClient.FabricV1().RouterLBRules().Update(context.Background(), updated, metav1.UpdateOptions{})
	return err
}

func (c *Controller) deleteLoadBalancerChildSwitchRule(name string) error {
	if err := c.config.FabricClient.FabricV1().SwitchLBRules().Delete(context.Background(), name, metav1.DeleteOptions{}); err != nil && !k8serrors.IsNotFound(err) {
		return err
	}
	return nil
}

func (c *Controller) deleteLoadBalancerChildRouterRule(name string) error {
	if err := c.config.FabricClient.FabricV1().RouterLBRules().Delete(context.Background(), name, metav1.DeleteOptions{}); err != nil && !k8serrors.IsNotFound(err) {
		return err
	}
	return nil
}

func (c *Controller) syncLoadBalancerStatusFromSwitchRule(lb *fabricv1.LoadBalancer, child, vip string) error {
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
	_, err = c.config.FabricClient.FabricV1().LoadBalancers().UpdateStatus(context.Background(), updated, metav1.UpdateOptions{})
	return err
}

func (c *Controller) patchLoadBalancerStatus(lb *fabricv1.LoadBalancer, vip, reason, message string) error {
	updated := lb.DeepCopy()
	updated.Status.Vip = vip
	if _, err := c.config.FabricClient.FabricV1().LoadBalancers().UpdateStatus(context.Background(), updated, metav1.UpdateOptions{}); err != nil {
		klog.Errorf("update LoadBalancer %s status: %v", lb.Name, err)
		return err
	}
	if message != "" {
		klog.V(3).Infof("LoadBalancer %s: %s: %s", lb.Name, reason, message)
	}
	return nil
}

func toSwitchLBRulePorts(ports []fabricv1.LoadBalancerPort) []fabricv1.SwitchLBRulePort {
	out := make([]fabricv1.SwitchLBRulePort, 0, len(ports))
	for _, p := range ports {
		out = append(out, fabricv1.SwitchLBRulePort(p))
	}
	return out
}

func toRouterLBRulePorts(ports []fabricv1.LoadBalancerPort) []fabricv1.RouterLBRulePort {
	out := make([]fabricv1.RouterLBRulePort, 0, len(ports))
	for _, p := range ports {
		out = append(out, fabricv1.RouterLBRulePort(p))
	}
	return out
}
