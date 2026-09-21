package ovs

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/ovn-kubernetes/libovsdb/ovsdb"
	"k8s.io/klog/v2"

	"github.com/cloudyfolks-labs/fabric/pkg/ovsdb/ovnnb"
	"github.com/cloudyfolks-labs/fabric/pkg/util"
	"github.com/cloudyfolks-labs/fabric/versions"
)

const (
	fabricVersionKey = "fabric-version"
)

var (
	sgPortGroupPattern = regexp.MustCompile(`^ovn\.sg\..+`)

	sgAddressSetPattern = regexp.MustCompile(`^ovn\.sg\..+\.associated\.v[46]$`)

	npAddressSetPattern = regexp.MustCompile(`\.(ingress|egress)\.(allow|except)\.(ip[46]|all)(\.\d+)?$`)

	clusterLBPattern = regexp.MustCompile(`^cluster-(tcp|udp|sctp)(-session)?-loadbalancer$`)
	vpcLBPattern     = regexp.MustCompile(`^vpc-.+-(tcp|udp|sctp)-(load|sess-load)$`)
)

func (c *OVNNbClient) GetFabricVersion() (string, error) {
	nbGlobal, err := c.GetNbGlobal()
	if err != nil {
		return "", fmt.Errorf("failed to get NBGlobal: %w", err)
	}

	if nbGlobal.ExternalIDs == nil {
		return "", nil
	}

	return nbGlobal.ExternalIDs[fabricVersionKey], nil
}

func (c *OVNNbClient) SetFabricVersion(version string) error {
	nbGlobal, err := c.GetNbGlobal()
	if err != nil {
		return fmt.Errorf("failed to get NBGlobal: %w", err)
	}

	if nbGlobal.ExternalIDs == nil {
		nbGlobal.ExternalIDs = make(map[string]string)
	}

	if nbGlobal.ExternalIDs[fabricVersionKey] == version {
		return nil
	}

	nbGlobal.ExternalIDs[fabricVersionKey] = version
	if err := c.UpdateNbGlobal(nbGlobal, &nbGlobal.ExternalIDs); err != nil {
		return fmt.Errorf("failed to update NBGlobal with fabric version: %w", err)
	}

	klog.Infof("updated fabric version in NBGlobal to %s", version)
	return nil
}

func (c *OVNNbClient) needsVendorMigration() (bool, error) {
	storedVersion, err := c.GetFabricVersion()
	if err != nil {
		return false, err
	}

	if storedVersion == "" {
		klog.Info("no fabric version found in NBGlobal, migration may be needed")
		return true, nil
	}

	if storedVersion == versions.VERSION {
		klog.Infof("stored version %s matches current version, skipping vendor migration", storedVersion)
		return false, nil
	}

	stored := strings.TrimPrefix(storedVersion, "v")
	vendorTagVersion := "1.15.0"

	if util.CompareVersion(stored, vendorTagVersion) < 0 {
		klog.Infof("stored version %s is older than %s, vendor migration needed", storedVersion, vendorTagVersion)
		return true, nil
	}

	klog.Infof("stored version %s is >= %s, skipping vendor migration", storedVersion, vendorTagVersion)
	return false, nil
}

func (c *OVNNbClient) MigrateVendorExternalIDs() error {
	needsMigration, err := c.needsVendorMigration()
	if err != nil {
		klog.Errorf("failed to check if vendor migration is needed: %v", err)
		return err
	}

	if !needsMigration {
		return c.SetFabricVersion(versions.VERSION)
	}

	klog.Info("starting migration of vendor externalIDs to fabric resources")

	fabricRouters, err := c.getFabricRouterNames()
	if err != nil {
		klog.Errorf("failed to get fabric router names: %v", err)
		return err
	}
	klog.Infof("found %d fabric logical routers", len(fabricRouters))

	fabricSwitches, err := c.getFabricSwitchNames()
	if err != nil {
		klog.Errorf("failed to get fabric switch names: %v", err)
		return err
	}
	klog.Infof("found %d fabric logical switches", len(fabricSwitches))

	if err := c.migrateLogicalRouterPorts(fabricRouters); err != nil {
		return err
	}

	if err := c.migratePortGroups(); err != nil {
		return err
	}

	if err := c.migrateAddressSets(); err != nil {
		return err
	}

	if err := c.migrateLoadBalancers(); err != nil {
		return err
	}

	if err := c.migrateACLs(fabricSwitches); err != nil {
		return err
	}

	klog.Info("completed migration of vendor externalIDs")

	if err := c.SetFabricVersion(versions.VERSION); err != nil {
		klog.Errorf("failed to store fabric version after migration: %v", err)
		return err
	}

	return nil
}

func (c *OVNNbClient) getFabricRouterNames() (map[string]bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), c.Timeout)
	defer cancel()

	var lrList []ovnnb.LogicalRouter
	if err := c.ovsDbClient.WhereCache(func(lr *ovnnb.LogicalRouter) bool {
		if len(lr.ExternalIDs) > 0 && lr.ExternalIDs["vendor"] == util.VendorTag {
			return true
		}
		return false
	}).List(ctx, &lrList); err != nil {
		return nil, fmt.Errorf("failed to list logical routers: %w", err)
	}

	names := make(map[string]bool, len(lrList))
	for _, lr := range lrList {
		names[lr.Name] = true
	}
	return names, nil
}

func (c *OVNNbClient) getFabricSwitchNames() (map[string]bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), c.Timeout)
	defer cancel()

	var lsList []ovnnb.LogicalSwitch
	if err := c.ovsDbClient.WhereCache(func(ls *ovnnb.LogicalSwitch) bool {
		if len(ls.ExternalIDs) > 0 && ls.ExternalIDs["vendor"] == util.VendorTag {
			return true
		}
		return false
	}).List(ctx, &lsList); err != nil {
		return nil, fmt.Errorf("failed to list logical switches: %w", err)
	}

	names := make(map[string]bool, len(lsList))
	for _, ls := range lsList {
		names[ls.Name] = true
	}
	return names, nil
}

func (c *OVNNbClient) migrateLogicalRouterPorts(fabricRouters map[string]bool) error {
	ctx, cancel := context.WithTimeout(context.Background(), c.Timeout)
	defer cancel()

	var lrpList []ovnnb.LogicalRouterPort
	if err := c.ovsDbClient.WhereCache(func(lrp *ovnnb.LogicalRouterPort) bool {
		if len(lrp.ExternalIDs) > 0 && lrp.ExternalIDs["vendor"] == util.VendorTag {
			return false
		}

		if len(lrp.ExternalIDs) > 0 {
			if lrName, ok := lrp.ExternalIDs[logicalRouterKey]; ok && fabricRouters[lrName] {
				return true
			}
		}
		return false
	}).List(ctx, &lrpList); err != nil {
		return fmt.Errorf("failed to list logical router ports for migration: %w", err)
	}

	if len(lrpList) == 0 {
		klog.Info("no logical router ports need vendor migration")
		return nil
	}

	klog.Infof("migrating %d logical router ports to add vendor tag", len(lrpList))

	ops := make([]ovsdb.Operation, 0, len(lrpList))
	for i := range lrpList {
		lrp := &lrpList[i]
		if lrp.ExternalIDs == nil {
			lrp.ExternalIDs = make(map[string]string)
		}
		lrp.ExternalIDs["vendor"] = util.VendorTag

		op, err := c.Where(lrp).Update(lrp, &lrp.ExternalIDs)
		if err != nil {
			klog.Errorf("failed to generate update operation for LRP %s: %v", lrp.Name, err)
			continue
		}
		ops = append(ops, op...)
	}

	if len(ops) == 0 {
		return nil
	}

	if err := c.Transact("lrp-vendor-migrate", ops); err != nil {
		return fmt.Errorf("failed to migrate logical router port vendor tags: %w", err)
	}

	klog.Infof("successfully migrated %d logical router ports", len(lrpList))
	return nil
}

func (c *OVNNbClient) migratePortGroups() error {
	ctx, cancel := context.WithTimeout(context.Background(), c.Timeout)
	defer cancel()

	var pgList []ovnnb.PortGroup
	if err := c.ovsDbClient.WhereCache(func(pg *ovnnb.PortGroup) bool {
		if len(pg.ExternalIDs) > 0 && pg.ExternalIDs["vendor"] == util.VendorTag {
			return false
		}

		if sgPortGroupPattern.MatchString(pg.Name) {
			return true
		}

		if len(pg.ExternalIDs) > 0 {
			if _, hasSg := pg.ExternalIDs[sgKey]; hasSg {
				return true
			}
			if _, hasType := pg.ExternalIDs["type"]; hasType {
				return true
			}
		}

		return false
	}).List(ctx, &pgList); err != nil {
		return fmt.Errorf("failed to list port groups for migration: %w", err)
	}

	if len(pgList) == 0 {
		klog.Info("no port groups need vendor migration")
		return nil
	}

	klog.Infof("migrating %d port groups to add vendor tag", len(pgList))

	ops := make([]ovsdb.Operation, 0, len(pgList))
	for i := range pgList {
		pg := &pgList[i]
		if pg.ExternalIDs == nil {
			pg.ExternalIDs = make(map[string]string)
		}
		pg.ExternalIDs["vendor"] = util.VendorTag

		op, err := c.Where(pg).Update(pg, &pg.ExternalIDs)
		if err != nil {
			klog.Errorf("failed to generate update operation for PortGroup %s: %v", pg.Name, err)
			continue
		}
		ops = append(ops, op...)
	}

	if len(ops) == 0 {
		return nil
	}

	if err := c.Transact("pg-vendor-migrate", ops); err != nil {
		return fmt.Errorf("failed to migrate port group vendor tags: %w", err)
	}

	klog.Infof("successfully migrated %d port groups", len(pgList))
	return nil
}

func (c *OVNNbClient) migrateAddressSets() error {
	ctx, cancel := context.WithTimeout(context.Background(), c.Timeout)
	defer cancel()

	var asList []ovnnb.AddressSet
	if err := c.ovsDbClient.WhereCache(func(as *ovnnb.AddressSet) bool {
		if len(as.ExternalIDs) > 0 && as.ExternalIDs["vendor"] == util.VendorTag {
			return false
		}

		if sgAddressSetPattern.MatchString(as.Name) {
			return true
		}

		if npAddressSetPattern.MatchString(as.Name) {
			return true
		}

		if len(as.ExternalIDs) > 0 {
			if _, hasSg := as.ExternalIDs[sgKey]; hasSg {
				return true
			}
		}

		return false
	}).List(ctx, &asList); err != nil {
		return fmt.Errorf("failed to list address sets for migration: %w", err)
	}

	if len(asList) == 0 {
		klog.Info("no address sets need vendor migration")
		return nil
	}

	klog.Infof("migrating %d address sets to add vendor tag", len(asList))

	ops := make([]ovsdb.Operation, 0, len(asList))
	for i := range asList {
		as := &asList[i]
		if as.ExternalIDs == nil {
			as.ExternalIDs = make(map[string]string)
		}
		as.ExternalIDs["vendor"] = util.VendorTag

		op, err := c.Where(as).Update(as, &as.ExternalIDs)
		if err != nil {
			klog.Errorf("failed to generate update operation for AddressSet %s: %v", as.Name, err)
			continue
		}
		ops = append(ops, op...)
	}

	if len(ops) == 0 {
		return nil
	}

	if err := c.Transact("as-vendor-migrate", ops); err != nil {
		return fmt.Errorf("failed to migrate address set vendor tags: %w", err)
	}

	klog.Infof("successfully migrated %d address sets", len(asList))
	return nil
}

func (c *OVNNbClient) migrateLoadBalancers() error {
	ctx, cancel := context.WithTimeout(context.Background(), c.Timeout)
	defer cancel()

	var lbList []ovnnb.LoadBalancer
	if err := c.ovsDbClient.WhereCache(func(lb *ovnnb.LoadBalancer) bool {
		if len(lb.ExternalIDs) > 0 && lb.ExternalIDs["vendor"] == util.VendorTag {
			return false
		}

		if clusterLBPattern.MatchString(lb.Name) {
			return true
		}

		if vpcLBPattern.MatchString(lb.Name) {
			return true
		}

		return false
	}).List(ctx, &lbList); err != nil {
		return fmt.Errorf("failed to list load balancers for migration: %w", err)
	}

	if len(lbList) == 0 {
		klog.Info("no load balancers need vendor migration")
		return nil
	}

	klog.Infof("migrating %d load balancers to add vendor tag", len(lbList))

	ops := make([]ovsdb.Operation, 0, len(lbList))
	for i := range lbList {
		lb := &lbList[i]
		if lb.ExternalIDs == nil {
			lb.ExternalIDs = make(map[string]string)
		}
		lb.ExternalIDs["vendor"] = util.VendorTag

		op, err := c.Where(lb).Update(lb, &lb.ExternalIDs)
		if err != nil {
			klog.Errorf("failed to generate update operation for LoadBalancer %s: %v", lb.Name, err)
			continue
		}
		ops = append(ops, op...)
	}

	if len(ops) == 0 {
		return nil
	}

	if err := c.Transact("lb-vendor-migrate", ops); err != nil {
		return fmt.Errorf("failed to migrate load balancer vendor tags: %w", err)
	}

	klog.Infof("successfully migrated %d load balancers", len(lbList))
	return nil
}

func (c *OVNNbClient) migrateACLs(fabricSwitches map[string]bool) error {
	ctx, cancel := context.WithTimeout(context.Background(), c.Timeout)
	defer cancel()

	fabricPortGroups := make(map[string]bool)
	var pgList []ovnnb.PortGroup
	if err := c.ovsDbClient.WhereCache(func(pg *ovnnb.PortGroup) bool {
		if len(pg.ExternalIDs) > 0 && pg.ExternalIDs["vendor"] == util.VendorTag {
			return true
		}

		if sgPortGroupPattern.MatchString(pg.Name) {
			return true
		}
		if len(pg.ExternalIDs) > 0 {
			if _, hasSg := pg.ExternalIDs[sgKey]; hasSg {
				return true
			}
			if _, hasType := pg.ExternalIDs["type"]; hasType {
				return true
			}
		}

		return false
	}).List(ctx, &pgList); err != nil {
		return fmt.Errorf("failed to list port groups: %w", err)
	}

	for _, pg := range pgList {
		fabricPortGroups[pg.Name] = true
	}

	var aclList []ovnnb.ACL
	if err := c.ovsDbClient.WhereCache(func(acl *ovnnb.ACL) bool {
		if len(acl.ExternalIDs) > 0 && acl.ExternalIDs["vendor"] == util.VendorTag {
			return false
		}

		if len(acl.ExternalIDs) > 0 {
			if parent, ok := acl.ExternalIDs[aclParentKey]; ok {
				if fabricPortGroups[parent] || fabricSwitches[parent] {
					return true
				}
			}

			if subnet, ok := acl.ExternalIDs["subnet"]; ok && fabricSwitches[subnet] {
				return true
			}
		}

		return false
	}).List(ctx, &aclList); err != nil {
		return fmt.Errorf("failed to list ACLs for migration: %w", err)
	}

	if len(aclList) == 0 {
		klog.Info("no ACLs need vendor migration")
		return nil
	}

	klog.Infof("migrating %d ACLs to add vendor tag", len(aclList))

	ops := make([]ovsdb.Operation, 0, len(aclList))
	for i := range aclList {
		acl := &aclList[i]
		if acl.ExternalIDs == nil {
			acl.ExternalIDs = make(map[string]string)
		}
		acl.ExternalIDs["vendor"] = util.VendorTag

		op, err := c.Where(acl).Update(acl, &acl.ExternalIDs)
		if err != nil {
			klog.Errorf("failed to generate update operation for ACL %s: %v", acl.UUID, err)
			continue
		}
		ops = append(ops, op...)
	}

	if len(ops) == 0 {
		return nil
	}

	if err := c.Transact("acl-vendor-migrate", ops); err != nil {
		return fmt.Errorf("failed to migrate ACL vendor tags: %w", err)
	}

	klog.Infof("successfully migrated %d ACLs", len(aclList))
	return nil
}
