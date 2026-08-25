package ovs

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/ovn-kubernetes/libovsdb/client"
	"github.com/ovn-kubernetes/libovsdb/model"
	"github.com/ovn-kubernetes/libovsdb/ovsdb"
	"k8s.io/klog/v2"

	ovsclient "github.com/cloudyfolks-labs/fabric/pkg/ovsdb/client"
	"github.com/cloudyfolks-labs/fabric/pkg/ovsdb/ovnnb"
)

func gatewayChassisPriorities(chassises []string) map[string]int {
	priorities := make(map[string]int, len(chassises))
	for i, chassis := range chassises {
		priorities[chassis] = 100 - i
	}
	return priorities
}

func (c *OVNNbClient) gatewayChassisMemberOps(lrpName string, chassises []string) ([]ovnnb.GatewayChassis, []ovsdb.Operation, error) {
	existing, err := c.ListGatewayChassisByLogicalRouterPort(lrpName, true)
	if err != nil {
		klog.Error(err)
		return nil, nil, err
	}

	missing := gatewayChassisPriorities(chassises)
	kept := make([]ovnnb.GatewayChassis, 0, len(existing))
	stale := make([]string, 0, len(existing))
	for _, gwChassis := range existing {
		if _, ok := missing[gwChassis.ChassisName]; !ok {
			stale = append(stale, gwChassis.UUID)
			continue
		}
		delete(missing, gwChassis.ChassisName)
		kept = append(kept, gwChassis)
	}

	var ops []ovsdb.Operation
	if len(stale) != 0 {
		removeOps, err := c.LogicalRouterPortUpdateGatewayChassisOp(lrpName, stale, ovsdb.MutateOperationDelete)
		if err != nil {
			klog.Error(err)
			return nil, nil, err
		}
		ops = append(ops, removeOps...)
	}

	if len(missing) != 0 {
		models := make([]model.Model, 0, len(missing))
		uuids := make([]string, 0, len(missing))
		for _, chassisName := range chassises {
			priority, ok := missing[chassisName]
			if !ok {
				continue
			}
			gwChassis := &ovnnb.GatewayChassis{
				UUID:        ovsclient.NamedUUID(),
				Name:        lrpName + "-" + chassisName,
				ChassisName: chassisName,
				Priority:    priority,
				ExternalIDs: map[string]string{"lrp": lrpName},
			}
			models = append(models, model.Model(gwChassis))
			uuids = append(uuids, gwChassis.UUID)
		}
		createOps, err := c.Create(models...)
		if err != nil {
			klog.Error(err)
			return nil, nil, fmt.Errorf("generate operations for creating gateway chassis: %w", err)
		}
		addOps, err := c.LogicalRouterPortUpdateGatewayChassisOp(lrpName, uuids, ovsdb.MutateOperationInsert)
		if err != nil {
			klog.Error(err)
			return nil, nil, err
		}
		ops = append(ops, createOps...)
		ops = append(ops, addOps...)
	}

	return kept, ops, nil
}

// UpdateGatewayChassisMembers converges the members of a logical router port on
// the given list and keeps the priority of the members that stay. Use it when
// another mechanism owns the priorities, for example the BFD status handler.
func (c *OVNNbClient) UpdateGatewayChassisMembers(lrpName string, chassises []string) error {
	_, ops, err := c.gatewayChassisMemberOps(lrpName, chassises)
	if err != nil {
		return err
	}
	if len(ops) == 0 {
		return nil
	}
	if err = c.Transact("gateway-chassises-update", ops); err != nil {
		err := fmt.Errorf("update gateway chassis members %v for logical router port %s: %w", chassises, lrpName, err)
		klog.Error(err)
		return err
	}
	return nil
}

// UpdateGatewayChassises converges the gateway chassis of a logical router port
// on the given ordered list: it rewrites the priorities, removes the members
// that are no longer wanted and adds the missing ones.
func (c *OVNNbClient) UpdateGatewayChassises(lrpName string, chassises []string) error {
	kept, ops, err := c.gatewayChassisMemberOps(lrpName, chassises)
	if err != nil {
		return err
	}

	wanted := gatewayChassisPriorities(chassises)
	for i := range kept {
		gwChassis := kept[i]
		priority := wanted[gwChassis.ChassisName]
		if gwChassis.Priority == priority {
			continue
		}
		gwChassis.Priority = priority
		updateOps, err := c.Where(&gwChassis).Update(&gwChassis, &gwChassis.Priority)
		if err != nil {
			klog.Error(err)
			return fmt.Errorf("generate operations for updating gateway chassis %s: %w", gwChassis.Name, err)
		}
		ops = append(ops, updateOps...)
	}

	if len(ops) == 0 {
		return nil
	}
	if err = c.Transact("gateway-chassises-update", ops); err != nil {
		err := fmt.Errorf("update gateway chassis %v for logical router port %s: %w", chassises, lrpName, err)
		klog.Error(err)
		return err
	}
	return nil
}

// DeleteGatewayChassisByChassisName removes every gateway chassis that points at
// a chassis that no longer exists, so no logical router port keeps a dangling
// reference to it.
func (c *OVNNbClient) DeleteGatewayChassisByChassisName(chassisName string) error {
	ctx, cancel := context.WithTimeout(context.Background(), c.Timeout)
	defer cancel()

	gwChassisList := make([]ovnnb.GatewayChassis, 0)
	if err := c.ovsDbClient.WhereCache(func(gwChassis *ovnnb.GatewayChassis) bool {
		return gwChassis.ChassisName == chassisName
	}).List(ctx, &gwChassisList); err != nil {
		if errors.Is(err, client.ErrNotFound) {
			return nil
		}
		err = fmt.Errorf("failed to list gateway chassis of chassis %s: %w", chassisName, err)
		klog.Error(err)
		return err
	}
	if len(gwChassisList) == 0 {
		return nil
	}

	byLrp := make(map[string][]string, len(gwChassisList))
	for _, gwChassis := range gwChassisList {
		lrpName := gwChassis.ExternalIDs["lrp"]
		if lrpName == "" {
			continue
		}
		byLrp[lrpName] = append(byLrp[lrpName], gwChassis.UUID)
	}

	var ops []ovsdb.Operation
	for lrpName, uuids := range byLrp {
		removeOps, err := c.LogicalRouterPortUpdateGatewayChassisOp(lrpName, uuids, ovsdb.MutateOperationDelete)
		if err != nil {
			klog.Error(err)
			return err
		}
		ops = append(ops, removeOps...)
	}
	if len(ops) == 0 {
		return nil
	}
	if err := c.Transact("gateway-chassises-del", ops); err != nil {
		err := fmt.Errorf("delete gateway chassis of chassis %s: %w", chassisName, err)
		klog.Error(err)
		return err
	}
	return nil
}

// UpdateGatewayChassis update gateway chassis
func (c *OVNNbClient) UpdateGatewayChassis(gwChassis *ovnnb.GatewayChassis, fields ...any) error {
	op, err := c.ovsDbClient.Where(gwChassis).Update(gwChassis, fields...)
	if err != nil {
		err := fmt.Errorf("failed to generate operations for gateway chassis %s with fields %v: %w", gwChassis.ChassisName, fields, err)
		klog.Error(err)
		return err
	}
	if err = c.Transact("gateway-chassis-update", op); err != nil {
		err := fmt.Errorf("failed to update gateway chassis %s: %w", gwChassis.ChassisName, err)
		klog.Error(err)
		return err
	}
	return nil
}

// ListGatewayChassisByLogicalRouterPort get gateway chassis by lrp name
func (c *OVNNbClient) ListGatewayChassisByLogicalRouterPort(lrpName string, ignoreNotFound bool) ([]ovnnb.GatewayChassis, error) {
	ctx, cancel := context.WithTimeout(context.Background(), c.Timeout)
	defer cancel()

	gwChassisList := make([]ovnnb.GatewayChassis, 0)
	if err := c.ovsDbClient.WhereCache(func(gwChassis *ovnnb.GatewayChassis) bool {
		if gwChassis.ExternalIDs != nil && gwChassis.ExternalIDs["lrp"] == lrpName {
			return true
		}
		return false
	}).List(ctx, &gwChassisList); err != nil {
		if ignoreNotFound && errors.Is(err, client.ErrNotFound) {
			return nil, nil
		}
		err = fmt.Errorf("failed to list gw chassis for lrp %s: %w", lrpName, err)
		klog.Error(err)
		return nil, err
	}

	slices.SortFunc(gwChassisList, func(a, b ovnnb.GatewayChassis) int {
		return b.Priority - a.Priority
	})
	return gwChassisList, nil
}

// GetGatewayChassis get gateway chassis by name
func (c *OVNNbClient) GetGatewayChassis(name string, ignoreNotFound bool) (*ovnnb.GatewayChassis, error) {
	ctx, cancel := context.WithTimeout(context.Background(), c.Timeout)
	defer cancel()

	gwChassis := &ovnnb.GatewayChassis{Name: name}
	if err := c.Get(ctx, gwChassis); err != nil {
		if ignoreNotFound && errors.Is(err, client.ErrNotFound) {
			return nil, nil
		}

		return nil, fmt.Errorf("get gateway chassis %s: %w", name, err)
	}

	return gwChassis, nil
}
