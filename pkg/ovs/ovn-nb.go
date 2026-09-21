package ovs

import (
	"fmt"
	"strings"

	"github.com/ovn-kubernetes/libovsdb/ovsdb"
	"k8s.io/klog/v2"

	ovsclient "github.com/cloudyfolks-labs/fabric/pkg/ovsdb/client"
	"github.com/cloudyfolks-labs/fabric/pkg/ovsdb/ovnnb"
	"github.com/cloudyfolks-labs/fabric/pkg/util"
)

const (
	logicalRouterKey      = "lr"
	LogicalSwitchKey      = "ls"
	portGroupKey          = "pg"
	aclParentKey          = "parent"
	associatedSgKeyPrefix = "associated_sg_"
	sgsKey                = "security_groups"
	sgKey                 = "sg"
	PortKey               = "port"
)

func (c *OVNNbClient) CreateGatewayLogicalSwitch(lsName, lrName, provider, ip, mac string, vlanID int, chassises ...string) error {
	lspName := fmt.Sprintf("%s-%s", lsName, lrName)
	lrpName := fmt.Sprintf("%s-%s", lrName, lsName)

	oldLocalnetLspName := "ln-" + lsName
	if err := c.DeleteLogicalSwitchPort(oldLocalnetLspName); err != nil {
		klog.Error(err)
		return fmt.Errorf("failed to delete old localnet %s: %w", oldLocalnetLspName, err)
	}

	localnetLspName := GetLocalnetName(lsName)
	if err := c.CreateBareLogicalSwitch(lsName); err != nil {
		klog.Error(err)
		return fmt.Errorf("create logical switch %s: %w", lsName, err)
	}

	if err := c.CreateLocalnetLogicalSwitchPort(lsName, localnetLspName, provider, "", vlanID); err != nil {
		klog.Error(err)
		return fmt.Errorf("create localnet logical switch port %s: %w", localnetLspName, err)
	}

	return c.CreateLogicalPatchPort(lsName, lrName, lspName, lrpName, ip, mac, chassises...)
}

func (c *OVNNbClient) CreateLogicalPatchPort(lsName, lrName, lspName, lrpName, ip, mac string, chassises ...string) error {
	if len(ip) != 0 {
		if err := util.CheckCidrs(ip); err != nil {
			err := fmt.Errorf("invalid ip %s: %w", ip, err)
			klog.Error(err)
			return err
		}
	}
	if mac == "" {
		mac = util.GenerateMac()
	}

	ops, err := c.CreateRouterPortOp(lsName, lrName, lspName, lrpName, ip, mac)
	if err != nil {
		err := fmt.Errorf("generate operations for creating patch port: %w", err)
		klog.Error(err)
		return err
	}

	if err = c.Transact("lrp-lsp-add", ops); err != nil {
		err := fmt.Errorf("create logical patch port %s and %s: %w", lspName, lrpName, err)
		klog.Error(err)
		return err
	}

	if err := c.UpdateGatewayChassises(lrpName, chassises); err != nil {
		err := fmt.Errorf("create gateway chassises for logical router port %s: %w", lrpName, err)
		klog.Error(err)
		return err
	}
	return nil
}

func (c *OVNNbClient) DeleteLogicalGatewaySwitch(lsName, lrName string) error {
	lrpName := fmt.Sprintf("%s-%s", lrName, lsName)

	lsDelOp, err := c.DeleteLogicalSwitchOp(lsName)
	if err != nil {
		klog.Error(err)
		return fmt.Errorf("generate operations for deleting gateway switch %s: %w", lsName, err)
	}

	lrpDelOp, err := c.DeleteLogicalRouterPortOp(lrpName)
	if err != nil {
		klog.Error(err)
		return fmt.Errorf("generate operations for deleting gateway router port %s: %w", lrpName, err)
	}

	ops := make([]ovsdb.Operation, 0, len(lsDelOp)+len(lrpDelOp))
	ops = append(ops, lsDelOp...)
	ops = append(ops, lrpDelOp...)

	if err = c.Transact("gw-ls-del", ops); err != nil {
		klog.Error(err)
		return fmt.Errorf("delete gateway switch %s: %w", lsName, err)
	}

	return nil
}

func (c *OVNNbClient) DeleteSecurityGroup(sgName string) error {
	pgName := GetSgPortGroupName(sgName)

	if err := c.DeleteAcls(pgName, portGroupKey, "", nil); err != nil {
		klog.Error(err)
		return fmt.Errorf("delete acls from port group %s: %w", pgName, err)
	}

	if err := c.DeleteAddressSets(map[string]string{sgKey: sgName}); err != nil {
		klog.Error(err)
		return err
	}

	if sgName == util.DefaultSecurityGroupName {
		if err := c.SetLogicalSwitchPortsSecurityGroup(sgName, "remove"); err != nil {
			klog.Error(err)
			return fmt.Errorf("clear default security group %s from logical switch ports: %w", sgName, err)
		}
	}

	return c.DeletePortGroup(pgName)
}

func (c *OVNNbClient) CreateRouterPortOp(lsName, lrName, lspName, lrpName, ip, mac string) ([]ovsdb.Operation, error) {
	lspExist, err := c.LogicalSwitchPortExists(lspName)
	if err != nil {
		klog.Error(err)
		return nil, err
	}

	if lspExist {
		return nil, nil
	}

	lsp := &ovnnb.LogicalSwitchPort{
		UUID:      ovsclient.NamedUUID(),
		Name:      lspName,
		Addresses: []string{"router"},
		Type:      "router",
		Options: map[string]string{
			"router-port": lrpName,
		},
	}

	lspCreateOp, err := c.CreateLogicalSwitchPortOp(lsp, lsName)
	if err != nil {
		klog.Error(err)
		return nil, err
	}

	lrp := &ovnnb.LogicalRouterPort{
		UUID:     ovsclient.NamedUUID(),
		Name:     lrpName,
		Networks: strings.Split(ip, ","),
		MAC:      mac,
		ExternalIDs: map[string]string{
			"vendor": util.VendorTag,
		},
	}

	lrpCreateOp, err := c.CreateLogicalRouterPortOp(lrp, lrName)
	if err != nil {
		klog.Error(err)
		return nil, err
	}

	ops := make([]ovsdb.Operation, 0, len(lspCreateOp)+len(lrpCreateOp))
	ops = append(ops, lspCreateOp...)
	ops = append(ops, lrpCreateOp...)

	return ops, nil
}

func (c *OVNNbClient) RemoveLogicalPatchPort(lspName, lrpName string) error {
	lsp, err := c.GetLogicalSwitchPort(lspName, true)
	if err != nil {
		klog.Error(err)
		return fmt.Errorf("failed to get logical switch port %s: %w", lspName, err)
	}
	var lspDelOp []ovsdb.Operation
	if lsp != nil {
		if lspDelOp, err = c.DeleteLogicalSwitchPortOp(lsp.ExternalIDs[LogicalSwitchKey], lsp.UUID); err != nil {
			klog.Error(err)
			return err
		}
	}

	lrpDelOp, err := c.DeleteLogicalRouterPortOp(lrpName)
	if err != nil {
		klog.Error(err)
		return err
	}

	ops := make([]ovsdb.Operation, 0, len(lspDelOp)+len(lrpDelOp))
	ops = append(ops, lspDelOp...)
	ops = append(ops, lrpDelOp...)

	if err = c.Transact("lrp-lsp-del", ops); err != nil {
		klog.Error(err)
		return fmt.Errorf("delete logical switch port %s and delete logical router port %s: %w", lspName, lrpName, err)
	}

	return nil
}
