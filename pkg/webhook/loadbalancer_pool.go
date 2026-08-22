package webhook

import (
	"context"
	"fmt"
	"net/http"

	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"
	ctrlwebhook "sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	ovnv1 "github.com/kubeovn/kube-ovn/pkg/apis/kubeovn/v1"
	"github.com/kubeovn/kube-ovn/pkg/util"
)

var loadBalancerPoolGVK = ovnv1.SchemeGroupVersion.WithKind(util.KindLoadBalancerPool)

func (v *ValidatingHook) LoadBalancerPoolCreateHook(ctx context.Context, req admission.Request) admission.Response {
	pool := ovnv1.LoadBalancerPool{}
	if err := v.decoder.DecodeRaw(req.Object, &pool); err != nil {
		return ctrlwebhook.Errored(http.StatusBadRequest, err)
	}
	return v.validateLoadBalancerPool(ctx, &pool)
}

func (v *ValidatingHook) LoadBalancerPoolUpdateHook(ctx context.Context, req admission.Request) admission.Response {
	pool := ovnv1.LoadBalancerPool{}
	if err := v.decoder.DecodeRaw(req.Object, &pool); err != nil {
		return ctrlwebhook.Errored(http.StatusBadRequest, err)
	}
	return v.validateLoadBalancerPool(ctx, &pool)
}

func (v *ValidatingHook) validateLoadBalancerPool(ctx context.Context, pool *ovnv1.LoadBalancerPool) admission.Response {
	subnet := ovnv1.Subnet{}
	if err := v.cache.Get(ctx, ctrlclient.ObjectKey{Name: pool.Spec.Subnet}, &subnet); err != nil {
		return ctrlwebhook.Denied(fmt.Sprintf("subnet %q of loadbalancer pool %q: %v", pool.Spec.Subnet, pool.Name, err))
	}

	if !pool.Spec.Default {
		return ctrlwebhook.Allowed("bypass")
	}

	poolList := &ovnv1.LoadBalancerPoolList{}
	if err := v.cache.List(ctx, poolList); err != nil {
		return ctrlwebhook.Errored(http.StatusInternalServerError, err)
	}
	for _, item := range poolList.Items {
		if item.Name != pool.Name && item.Spec.Default {
			return ctrlwebhook.Denied(fmt.Sprintf("loadbalancer pool %q is already the default pool", item.Name))
		}
	}

	return ctrlwebhook.Allowed("bypass")
}
