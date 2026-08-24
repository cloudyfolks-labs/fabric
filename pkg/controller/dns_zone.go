package controller

import (
	"context"
	"fmt"
	"net"
	"strings"

	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"

	"github.com/ovn-kubernetes/libovsdb/ovsdb"

	kubeovnv1 "github.com/kubeovn/kube-ovn/pkg/apis/kubeovn/v1"
)

func (c *Controller) enqueueAddDnsZone(obj any) {
	key := cache.MetaObjectToName(obj.(*kubeovnv1.DnsZone)).String()
	klog.V(3).Infof("enqueue add dns zone %s", key)
	c.addOrUpdateDnsZoneQueue.Add(key)
}

func (c *Controller) enqueueUpdateDnsZone(oldObj, newObj any) {
	oldZone := oldObj.(*kubeovnv1.DnsZone)
	newZone := newObj.(*kubeovnv1.DnsZone)
	if oldZone.ResourceVersion == newZone.ResourceVersion {
		return
	}
	key := cache.MetaObjectToName(newZone).String()
	klog.V(3).Infof("enqueue update dns zone %s", key)
	c.addOrUpdateDnsZoneQueue.Add(key)
}

func (c *Controller) enqueueDeleteDnsZone(obj any) {
	zone, ok := obj.(*kubeovnv1.DnsZone)
	if !ok {
		tombstone, ok := obj.(cache.DeletedFinalStateUnknown)
		if !ok {
			return
		}
		zone, ok = tombstone.Obj.(*kubeovnv1.DnsZone)
		if !ok {
			return
		}
	}
	klog.V(3).Infof("enqueue delete dns zone %s", zone.Name)
	c.delDnsZoneQueue.Add(zone.Name)
}

func (c *Controller) enqueueDnsZonesForVpc(vpcName string) {
	if c.dnsZoneLister == nil {
		return
	}
	zones, err := c.dnsZoneLister.List(labels.Everything())
	if err != nil {
		klog.Errorf("failed to list dns zones: %v", err)
		return
	}
	for _, zone := range zones {
		if zone.Spec.Vpc == vpcName {
			c.addOrUpdateDnsZoneQueue.Add(zone.Name)
		}
	}
}

func dnsZoneRecords(zone *kubeovnv1.DnsZone) (map[string]string, error) {
	records := make(map[string]string, len(zone.Spec.Records))
	for _, record := range zone.Spec.Records {
		name := strings.ToLower(strings.TrimSuffix(record.Name, "."))
		if name == "" {
			return nil, fmt.Errorf("record with empty name in dns zone %s", zone.Name)
		}
		for _, ip := range record.IPs {
			if net.ParseIP(ip) == nil {
				return nil, fmt.Errorf("record %s in dns zone %s has invalid ip %q", name, zone.Name, ip)
			}
		}
		records[name] = strings.Join(record.IPs, " ")
	}
	return records, nil
}

func (c *Controller) handleAddOrUpdateDnsZone(key string) error {
	c.dnsZoneKeyMutex.LockKey(key)
	defer func() { _ = c.dnsZoneKeyMutex.UnlockKey(key) }()

	cachedZone, err := c.dnsZoneLister.Get(key)
	if err != nil {
		if k8serrors.IsNotFound(err) {
			return nil
		}
		return err
	}
	klog.Infof("handle add/update dns zone %s", key)
	zone := cachedZone.DeepCopy()

	records, err := dnsZoneRecords(zone)
	if err != nil {
		klog.Error(err)
		return c.patchDnsZoneStatus(zone, 0, corev1.ConditionFalse, "InvalidRecords", err.Error())
	}

	if _, err = c.vpcsLister.Get(zone.Spec.Vpc); err != nil {
		klog.Errorf("failed to get vpc %s of dns zone %s: %v", zone.Spec.Vpc, key, err)
		if patchErr := c.patchDnsZoneStatus(zone, 0, corev1.ConditionFalse, "VpcNotFound", err.Error()); patchErr != nil {
			klog.Error(patchErr)
		}
		return err
	}

	dnsUUID, err := c.OVNNbClient.EnsureDnsZone(zone.Name, records)
	if err != nil {
		klog.Error(err)
		return err
	}

	subnets, err := c.subnetsLister.List(labels.Everything())
	if err != nil {
		klog.Error(err)
		return err
	}
	for _, subnet := range subnets {
		if subnet.Spec.Vpc != zone.Spec.Vpc {
			continue
		}
		if err = c.OVNNbClient.LogicalSwitchUpdateDnsRecords(subnet.Name, dnsUUID, ovsdb.MutateOperationInsert); err != nil {
			klog.Error(err)
			return err
		}
	}

	return c.patchDnsZoneStatus(zone, int32(len(records)), corev1.ConditionTrue, "Reconciled", "")
}

func (c *Controller) handleDelDnsZone(name string) error {
	c.dnsZoneKeyMutex.LockKey(name)
	defer func() { _ = c.dnsZoneKeyMutex.UnlockKey(name) }()

	klog.Infof("handle delete dns zone %s", name)
	return c.OVNNbClient.DeleteDnsZone(name)
}

func (c *Controller) patchDnsZoneStatus(zone *kubeovnv1.DnsZone, activeRecords int32, ready corev1.ConditionStatus, reason, message string) error {
	newZone := zone.DeepCopy()
	newZone.Status.ActiveRecords = activeRecords
	newZone.Status.Conditions = []kubeovnv1.Condition{{
		Type:               "Ready",
		Status:             ready,
		Reason:             reason,
		Message:            message,
		LastUpdateTime:     metav1.Now(),
		LastTransitionTime: metav1.Now(),
	}}
	for _, cond := range zone.Status.Conditions {
		if cond.Type == "Ready" && cond.Status == ready {
			newZone.Status.Conditions[0].LastTransitionTime = cond.LastTransitionTime
		}
	}

	if _, err := c.config.KubeOvnClient.FabricV1().DnsZones().UpdateStatus(context.Background(), newZone, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("failed to update status of dns zone %s: %w", zone.Name, err)
	}
	return nil
}

func (c *Controller) gcDnsZones() error {
	klog.Infof("start to gc dns zones")
	zones, err := c.dnsZoneLister.List(labels.Everything())
	if err != nil {
		return err
	}
	names := make(map[string]struct{}, len(zones))
	for _, zone := range zones {
		names[zone.Name] = struct{}{}
	}
	staleZones, err := c.OVNNbClient.ListDnsZoneNames()
	if err != nil {
		return err
	}
	for _, name := range staleZones {
		if _, ok := names[name]; ok {
			continue
		}
		klog.Infof("gc dns zone %s", name)
		if err = c.OVNNbClient.DeleteDnsZone(name); err != nil {
			return err
		}
	}
	klog.Infof("finish to gc dns zones")
	return nil
}
