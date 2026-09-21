package ovs

import (
	"context"
	"errors"
	"fmt"
	"maps"

	"github.com/ovn-kubernetes/libovsdb/model"
	"github.com/ovn-kubernetes/libovsdb/ovsdb"
	"k8s.io/klog/v2"
	"k8s.io/utils/set"

	fabricv1 "github.com/cloudyfolks-labs/fabric/pkg/apis/fabric/v1"
	ovsclient "github.com/cloudyfolks-labs/fabric/pkg/ovsdb/client"
	"github.com/cloudyfolks-labs/fabric/pkg/ovsdb/ovnnb"
)

func (c *OVNNbClient) AddNat(lrName, natType, externalIP, logicalIP, logicalMac, port, gatewayPort string, options map[string]string) error {
	if natType == ovnnb.NATTypeDNATAndSNAT {
		return c.addOrUpdateDnatAndSnat(lrName, externalIP, logicalIP, logicalMac, port, gatewayPort, options)
	}

	nat, err := c.newNat(lrName, natType, externalIP, logicalIP, logicalMac, port, func(nat *ovnnb.NAT) {
		setNatGatewayPort(nat, gatewayPort)
		if len(options) == 0 {
			return
		}
		if len(nat.Options) == 0 {
			nat.Options = make(map[string]string, len(options))
		}
		maps.Copy(nat.Options, options)
	})
	if err != nil {
		klog.Errorf("failed to new nat: %v", err)
		return err
	}
	if nat == nil {
		return c.EnsureNatGatewayPort(lrName, natType, externalIP, logicalIP, gatewayPort)
	}

	return c.CreateNats(lrName, nat)
}

func (c *OVNNbClient) EnsureNatGatewayPort(lrName, natType, externalIP, logicalIP, gatewayPort string) error {
	if gatewayPort == "" {
		return nil
	}

	nat, err := c.GetNat(lrName, natType, externalIP, logicalIP, true)
	if err != nil {
		klog.Error(err)
		return err
	}
	if nat == nil || (nat.GatewayPort != nil && *nat.GatewayPort == gatewayPort) {
		return nil
	}

	nat.GatewayPort = &gatewayPort
	if err = c.UpdateNat(nat, &nat.GatewayPort); err != nil {
		klog.Error(err)
		return fmt.Errorf("set gateway port %s on nat 'type %s external ip %s logical ip %s' of logical router %s: %w", gatewayPort, natType, externalIP, logicalIP, lrName, err)
	}

	return nil
}

func setNatGatewayPort(nat *ovnnb.NAT, gatewayPort string) {
	if gatewayPort != "" {
		nat.GatewayPort = &gatewayPort
	}
}

func (c *OVNNbClient) addOrUpdateDnatAndSnat(lrName, externalIP, logicalIP, logicalMac, port, gatewayPort string, options map[string]string) error {
	if externalIP == "" {
		err := fmt.Errorf("external ip is required when nat type is %s", ovnnb.NATTypeDNATAndSNAT)
		klog.Error(err)
		return err
	}

	if err := c.DeleteNat(lrName, ovnnb.NATTypeDNATAndSNAT, externalIP, ""); err != nil {
		klog.Errorf("failed to clear prior dnat_and_snat external_ip=%s: %v", externalIP, err)
		return err
	}

	nat := &ovnnb.NAT{
		UUID:       ovsclient.NamedUUID(),
		Type:       ovnnb.NATTypeDNATAndSNAT,
		ExternalIP: externalIP,
		LogicalIP:  logicalIP,
	}
	if logicalMac != "" {
		nat.ExternalMAC = &logicalMac
	}
	if port != "" {
		nat.LogicalPort = &port
	}
	setNatGatewayPort(nat, gatewayPort)
	if len(options) > 0 {
		nat.Options = make(map[string]string, len(options))
		maps.Copy(nat.Options, options)
	}

	klog.V(2).Infof("installing dnat_and_snat external_ip=%s logical_ip=%s logical_port=%s gateway_port=%s",
		externalIP, logicalIP, port, gatewayPort)
	return c.CreateNats(lrName, nat)
}

func (c *OVNNbClient) CreateNats(lrName string, nats ...*ovnnb.NAT) error {
	if len(nats) == 0 {
		err := errors.New("nats is empty")
		klog.Error(err)
		return err
	}

	models := make([]model.Model, 0, len(nats))
	natUUIDs := make([]string, 0, len(nats))
	for _, nat := range nats {
		if nat != nil {
			models = append(models, model.Model(nat))
			natUUIDs = append(natUUIDs, nat.UUID)
		}
	}

	createNatsOp, err := c.Create(models...)
	if err != nil {
		klog.Error(err)
		return fmt.Errorf("generate operations for creating nats: %w", err)
	}

	natAddOp, err := c.LogicalRouterUpdateNatOp(lrName, natUUIDs, ovsdb.MutateOperationInsert)
	if err != nil {
		klog.Error(err)
		return fmt.Errorf("generate operations for adding nats to logical router %s: %w", lrName, err)
	}

	ops := make([]ovsdb.Operation, 0, len(createNatsOp)+len(natAddOp))
	ops = append(ops, createNatsOp...)
	ops = append(ops, natAddOp...)

	if err = c.Transact("lr-nats-add", ops); err != nil {
		klog.Error(err)
		return fmt.Errorf("add nats to %s: %w", lrName, err)
	}

	return nil
}

func (c *OVNNbClient) EnsureSnat(lrName, externalIP, logicalIP string) error {
	if externalIP == "" {
		err := errors.New("snat external ip is required")
		klog.Error(err)
		return err
	}
	if logicalIP == "" {
		err := errors.New("snat logical ip is required")
		klog.Error(err)
		return err
	}

	natType := ovnnb.NATTypeSNAT
	nat, err := c.GetNat(lrName, natType, externalIP, logicalIP, true)
	if err != nil {
		klog.Error(err)
		return err
	}

	if nat != nil {
		return nil
	}

	if nat, err = c.newNat(lrName, natType, externalIP, logicalIP, "", ""); err != nil {
		klog.Error(err)
		return fmt.Errorf("new logical router %s nat 'type %s external ip %s logical ip %s': %w", lrName, natType, externalIP, logicalIP, err)
	}

	if err := c.CreateNats(lrName, nat); err != nil {
		klog.Error(err)
		return fmt.Errorf("add nat 'type %s external ip %s logical ip %s' to logical router %s: %w", natType, externalIP, logicalIP, lrName, err)
	}

	return nil
}

func (c *OVNNbClient) UpdateDnatAndSnat(lrName, externalIP, logicalIP, lspName, externalMac, gatewayType string) error {
	if externalIP == "" {
		err := errors.New("nat external ip is required")
		klog.Error(err)
		return err
	}
	if logicalIP == "" {
		err := errors.New("nat logical ip is required")
		klog.Error(err)
		return err
	}
	natType := ovnnb.NATTypeDNATAndSNAT

	nat, err := c.GetNat(lrName, natType, externalIP, "", true)
	if err != nil {
		klog.Error(err)
		return err
	}

	if nat != nil {
		if gatewayType == fabricv1.GWDistributedType {
			nat.LogicalPort = &lspName
			nat.ExternalMAC = &externalMac
			return c.UpdateNat(nat, &nat.LogicalPort, &nat.ExternalMAC)
		}
		return nil
	}

	options := func(nat *ovnnb.NAT) {
		if gatewayType == fabricv1.GWDistributedType {
			nat.LogicalPort = &lspName
			nat.ExternalMAC = &externalMac

			if nat.Options == nil {
				nat.Options = make(map[string]string, 1)
			}
			nat.Options["stateless"] = "true"
		}
	}

	if nat, err = c.newNat(lrName, natType, externalIP, logicalIP, "", "", options); err != nil {
		klog.Error(err)
		return fmt.Errorf("new logical router %s nat 'type %s external ip %s logical ip %s logical port %s external mac %s': %w", lrName, natType, externalIP, logicalIP, lspName, externalMac, err)
	}

	if err := c.CreateNats(lrName, nat); err != nil {
		klog.Error(err)
		return fmt.Errorf("add nat 'type %s external ip %s logical ip %s logical port %s external mac %s' to logical router %s: %w", natType, externalIP, logicalIP, lspName, externalMac, lrName, err)
	}

	return nil
}

func (c *OVNNbClient) UpdateNat(nat *ovnnb.NAT, fields ...any) error {
	if nat == nil {
		return errors.New("nat is nil")
	}

	op, err := c.ovsDbClient.Where(nat).Update(nat, fields...)
	if err != nil {
		klog.Error(err)
		return fmt.Errorf("generate operations for updating nat 'type %s external ip %s logical ip %s': %w", nat.Type, nat.ExternalIP, nat.LogicalIP, err)
	}

	if err = c.Transact("net-update", op); err != nil {
		klog.Error(err)
		return fmt.Errorf("update nat 'type %s external ip %s logical ip %s': %w", nat.Type, nat.ExternalIP, nat.LogicalIP, err)
	}

	return nil
}

func (c *OVNNbClient) DeleteNats(lrName, natType, logicalIP string) error {
	nats, err := c.ListNats(lrName, natType, logicalIP, nil)
	if err != nil {
		klog.Error(err)
		return fmt.Errorf("list logical router %s nats 'type %s logical ip %s': %w", lrName, natType, logicalIP, err)
	}

	natsUUIDs := make([]string, 0, len(nats))
	for _, nat := range nats {
		natsUUIDs = append(natsUUIDs, nat.UUID)
	}

	ops, err := c.LogicalRouterUpdateNatOp(lrName, natsUUIDs, ovsdb.MutateOperationDelete)
	if err != nil {
		klog.Error(err)
		return fmt.Errorf("generate operations for deleting nats from logical router %s: %w", lrName, err)
	}
	if err = c.Transact("nats-del", ops); err != nil {
		klog.Error(err)
		return fmt.Errorf("del nats from logical router %s: %w", lrName, err)
	}

	return nil
}

func (c *OVNNbClient) DeleteNat(lrName, natType, externalIP, logicalIP string) error {
	nat, err := c.GetNat(lrName, natType, externalIP, logicalIP, true)
	if err != nil {
		klog.Error(err)
		return err
	}
	if nat == nil {
		return nil
	}

	ops, err := c.LogicalRouterUpdateNatOp(lrName, []string{nat.UUID}, ovsdb.MutateOperationDelete)
	if err != nil {
		klog.Error(err)
		return fmt.Errorf("generate operations for deleting nat from logical router %s: %w", lrName, err)
	}
	if err = c.Transact("lr-nat-del", ops); err != nil {
		klog.Error(err)
		return fmt.Errorf("del nat from logical router %s: %w", lrName, err)
	}

	return nil
}

func (c *OVNNbClient) GetNATByUUID(uuid string) (*ovnnb.NAT, error) {
	ctx, cancel := context.WithTimeout(context.Background(), c.Timeout)
	defer cancel()

	nat := &ovnnb.NAT{UUID: uuid}
	if err := c.Get(ctx, nat); err != nil {
		klog.Error(err)
		return nil, err
	}

	return nat, nil
}

func (c *OVNNbClient) GetNat(lrName, natType, externalIP, logicalIP string, ignoreNotFound bool) (*ovnnb.NAT, error) {
	if len(lrName) == 0 {
		err := errors.New("the logical router name is required")
		klog.Error(err)
		return nil, err
	}
	if natType == ovnnb.NATTypeDNAT {
		err := errors.New("does not support dnat for now")
		klog.Error(err)
		return nil, err
	}

	if natType != ovnnb.NATTypeSNAT && natType != ovnnb.NATTypeDNATAndSNAT {
		err := errors.New("nat type must be one of [ snat, dnat_and_snat ]")
		klog.Error(err)
		return nil, err
	}

	if natType == ovnnb.NATTypeSNAT {
		if logicalIP == "" {
			err := fmt.Errorf("logical ip is required when nat type is %s", natType)
			klog.Error(err)
			return nil, err
		}
		if externalIP == "" {
			err := fmt.Errorf("external ip is required when nat type is %s", natType)
			klog.Error(err)
			return nil, err
		}
	}
	if natType == ovnnb.NATTypeDNATAndSNAT {
		if externalIP == "" {
			err := fmt.Errorf("external ip is required when nat type is %s", natType)
			klog.Error(err)
			return nil, err
		}
	}

	fnFilter := func(nat *ovnnb.NAT) bool {
		if natType == "" {
			return nat.LogicalIP == logicalIP
		}
		if natType == ovnnb.NATTypeSNAT {
			return nat.Type == natType && nat.ExternalIP == externalIP && nat.LogicalIP == logicalIP
		}

		if natType == ovnnb.NATTypeDNATAndSNAT {
			if nat.Type != natType || nat.ExternalIP != externalIP {
				return false
			}
			if logicalIP != "" && nat.LogicalIP != logicalIP {
				return false
			}
			return true
		}
		return nat.Type == natType && nat.ExternalIP == externalIP
	}
	natList, err := c.listLogicalRouterNatByFilter(lrName, fnFilter)
	if err != nil {
		klog.Error(err)
		return nil, fmt.Errorf("get logical router %s nat 'type %s external ip %s logical ip %s': %w", lrName, natType, externalIP, logicalIP, err)
	}

	if len(natList) == 0 {
		if ignoreNotFound {
			return nil, nil
		}
		err := fmt.Errorf("not found logical router %s nat 'type %s external ip %s logical ip %s'", lrName, natType, externalIP, logicalIP)
		klog.Error(err)
		return nil, err
	}

	if len(natList) > 1 {
		err := fmt.Errorf("more than one nat 'type %s external ip %s logical ip %s' in logical router %s", natType, externalIP, logicalIP, lrName)
		klog.Error(err)
		return nil, err
	}

	return natList[0], nil
}

func (c *OVNNbClient) ListNats(lrName, natType, logicalIP string, externalIDs map[string]string) ([]*ovnnb.NAT, error) {
	return c.listLogicalRouterNatByFilter(lrName, natFilter(natType, logicalIP, externalIDs))
}

func (c *OVNNbClient) NatExists(lrName, natType, externalIP, logicalIP string) (bool, error) {
	nat, err := c.GetNat(lrName, natType, externalIP, logicalIP, true)
	return nat != nil, err
}

func (c *OVNNbClient) newNat(lrName, natType, externalIP, logicalIP, logicalMac, port string, options ...func(nat *ovnnb.NAT)) (*ovnnb.NAT, error) {
	if len(lrName) == 0 {
		err := errors.New("the logical router name is required")
		klog.Error(err)
		return nil, err
	}

	if natType == ovnnb.NATTypeDNAT {
		err := errors.New("does not support dnat for now")
		klog.Error(err)
		return nil, err
	}

	if natType != ovnnb.NATTypeSNAT && natType != ovnnb.NATTypeDNATAndSNAT {
		err := errors.New("nat type must be one of [ snat, dnat_and_snat ]")
		klog.Error(err)
		return nil, err
	}

	if natType == ovnnb.NATTypeSNAT {
		if logicalIP == "" {
			err := fmt.Errorf("logical ip is required when nat type is %s", natType)
			klog.Error(err)
			return nil, err
		}
		if externalIP == "" {
			err := fmt.Errorf("external ip is required when nat type is %s", natType)
			klog.Error(err)
			return nil, err
		}
	}
	if natType == ovnnb.NATTypeDNATAndSNAT {
		if externalIP == "" {
			err := fmt.Errorf("external ip is required when nat type is %s", natType)
			klog.Error(err)
			return nil, err
		}
	}

	exists, err := c.NatExists(lrName, natType, externalIP, logicalIP)
	if err != nil {
		klog.Error(err)
		return nil, fmt.Errorf("get logical router %s nat: %w", lrName, err)
	}

	if exists {
		return nil, nil
	}

	nat := &ovnnb.NAT{
		UUID:       ovsclient.NamedUUID(),
		Type:       natType,
		ExternalIP: externalIP,
		LogicalIP:  logicalIP,
	}
	if logicalMac != "" {
		nat.ExternalMAC = &logicalMac
	}
	if port != "" {
		nat.LogicalPort = &port
	}

	for _, option := range options {
		option(nat)
	}

	return nat, nil
}

func natFilter(natType, logicalIP string, externalIDs map[string]string) func(nat *ovnnb.NAT) bool {
	return func(nat *ovnnb.NAT) bool {
		if len(nat.ExternalIDs) < len(externalIDs) {
			return false
		}

		if len(nat.ExternalIDs) != 0 {
			for k, v := range externalIDs {
				if len(v) == 0 {
					if len(nat.ExternalIDs[k]) == 0 {
						return false
					}
				} else {
					if nat.ExternalIDs[k] != v {
						return false
					}
				}
			}
		}

		if len(natType) != 0 && nat.Type != natType {
			return false
		}

		if len(logicalIP) != 0 && nat.LogicalIP != logicalIP {
			return false
		}

		return true
	}
}

func (c *OVNNbClient) listLogicalRouterNatByFilter(lrName string, filter func(route *ovnnb.NAT) bool) ([]*ovnnb.NAT, error) {
	lr, err := c.GetLogicalRouter(lrName, false)
	if err != nil {
		klog.Error(err)
		return nil, err
	}

	if len(lr.Nat) == 0 {
		return nil, nil
	}

	uuidSet := set.New(lr.Nat...)
	predicate := func(nat *ovnnb.NAT) bool {
		if !uuidSet.Has(nat.UUID) {
			return false
		}
		return filter == nil || filter(nat)
	}

	natList := make([]*ovnnb.NAT, 0, len(lr.Nat))
	if err = c.WhereCache(predicate).List(context.Background(), &natList); err != nil {
		klog.Error(err)
		return nil, err
	}

	return natList, nil
}
