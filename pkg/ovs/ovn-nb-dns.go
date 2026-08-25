package ovs

import (
	"context"
	"fmt"
	"maps"

	"github.com/ovn-kubernetes/libovsdb/model"
	"github.com/ovn-kubernetes/libovsdb/ovsdb"

	ovsclient "github.com/kubeovn/kube-ovn/pkg/ovsdb/client"
	"github.com/kubeovn/kube-ovn/pkg/ovsdb/ovnnb"
)

const dnsZoneExternalIDKey = "dns-zone"

func (c *OVNNbClient) listDNSZone(zone string) ([]ovnnb.DNS, error) {
	ctx, cancel := context.WithTimeout(context.Background(), c.Timeout)
	defer cancel()

	dnsList := make([]ovnnb.DNS, 0, 1)
	if err := c.ovsDbClient.WhereCache(func(dns *ovnnb.DNS) bool {
		return dns.ExternalIDs[dnsZoneExternalIDKey] == zone
	}).List(ctx, &dnsList); err != nil {
		return nil, fmt.Errorf("failed to list dns records of zone %s: %w", zone, err)
	}
	return dnsList, nil
}

func (c *OVNNbClient) GetDNSZone(zone string) (*ovnnb.DNS, error) {
	dnsList, err := c.listDNSZone(zone)
	if err != nil {
		return nil, err
	}
	if len(dnsList) == 0 {
		return nil, nil
	}
	return &dnsList[0], nil
}

func (c *OVNNbClient) EnsureDNSZone(zone string, records map[string]string) (string, error) {
	dnsList, err := c.listDNSZone(zone)
	if err != nil {
		return "", err
	}
	for i := 1; i < len(dnsList); i++ {
		if err = c.deleteDNSRow(&dnsList[i]); err != nil {
			return "", err
		}
	}
	var dns *ovnnb.DNS
	if len(dnsList) > 0 {
		dns = &dnsList[0]
	}

	if dns == nil {
		dns = &ovnnb.DNS{
			UUID: ovsclient.NamedUUID(),
			ExternalIDs: map[string]string{
				"vendor":             "kube-ovn",
				dnsZoneExternalIDKey: zone,
			},
			Records: records,
		}
		ops, err := c.ovsDbClient.Create(dns)
		if err != nil {
			return "", fmt.Errorf("failed to generate operations for creating dns zone %s: %w", zone, err)
		}
		if err = c.Transact("dns-create", ops); err != nil {
			return "", fmt.Errorf("failed to create dns zone %s: %w", zone, err)
		}
		dns, err = c.GetDNSZone(zone)
		if err != nil {
			return "", err
		}
		if dns == nil {
			return "", fmt.Errorf("dns zone %s not found after creation", zone)
		}
		return dns.UUID, nil
	}

	if maps.Equal(dns.Records, records) {
		return dns.UUID, nil
	}
	dns.Records = records
	ops, err := c.ovsDbClient.Where(dns).Update(dns, &dns.Records)
	if err != nil {
		return "", fmt.Errorf("failed to generate operations for updating dns zone %s: %w", zone, err)
	}
	if err = c.Transact("dns-update", ops); err != nil {
		return "", fmt.Errorf("failed to update dns zone %s: %w", zone, err)
	}
	return dns.UUID, nil
}

func (c *OVNNbClient) DeleteDNSZone(zone string) error {
	dnsList, err := c.listDNSZone(zone)
	if err != nil {
		return err
	}
	for i := range dnsList {
		if err = c.deleteDNSRow(&dnsList[i]); err != nil {
			return err
		}
	}
	return nil
}

func (c *OVNNbClient) deleteDNSRow(dns *ovnnb.DNS) error {
	ops, err := c.ovsDbClient.Where(dns).Delete()
	if err != nil {
		return fmt.Errorf("failed to generate operations for deleting dns row %s: %w", dns.UUID, err)
	}
	if err = c.Transact("dns-del", ops); err != nil {
		return fmt.Errorf("failed to delete dns row %s: %w", dns.UUID, err)
	}
	return nil
}

func (c *OVNNbClient) LogicalSwitchUpdateDNSRecords(lsName, dnsUUID string, op ovsdb.Mutator) error {
	ls, err := c.GetLogicalSwitch(lsName, true)
	if err != nil {
		return err
	}
	if ls == nil {
		return nil
	}

	ops, err := c.ovsDbClient.Where(ls).Mutate(ls, model.Mutation{
		Field:   &ls.DNSRecords,
		Value:   []string{dnsUUID},
		Mutator: op,
	})
	if err != nil {
		return fmt.Errorf("failed to generate operations for mutating dns records of logical switch %s: %w", lsName, err)
	}
	if err = c.Transact("ls-dns-update", ops); err != nil {
		return fmt.Errorf("failed to update dns records of logical switch %s: %w", lsName, err)
	}
	return nil
}

func (c *OVNNbClient) ListDNSZoneNames() ([]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), c.Timeout)
	defer cancel()

	dnsList := make([]ovnnb.DNS, 0)
	if err := c.ovsDbClient.WhereCache(func(dns *ovnnb.DNS) bool {
		return dns.ExternalIDs[dnsZoneExternalIDKey] != ""
	}).List(ctx, &dnsList); err != nil {
		return nil, fmt.Errorf("failed to list dns zones: %w", err)
	}
	names := make([]string, 0, len(dnsList))
	for _, dns := range dnsList {
		names = append(names, dns.ExternalIDs[dnsZoneExternalIDKey])
	}
	return names, nil
}
