package frr

import (
	"strings"
	"testing"
)

func TestReloadScriptAppliesEveryChangeByReload(t *testing.T) {
	if !strings.Contains(reloadScript, "--reload --overwrite") {
		t.Errorf("expected changes to be applied by frr-reload:\n%s", reloadScript)
	}
	for _, forbidden := range []string{"exit 0", "restart", "router bgp"} {
		if strings.Contains(reloadScript, forbidden) {
			t.Errorf("the reload loop must never leave the FRR container, found %q:\n%s", forbidden, reloadScript)
		}
	}
	if !strings.Contains(reloadScript, "--test") {
		t.Errorf("expected the desired config to be tested before the reload:\n%s", reloadScript)
	}
}

func TestReloadScriptSnapshotsBgpStateOnlyWhenBgpdIsUp(t *testing.T) {
	for _, expected := range []string{
		"[ ! -S /var/run/frr/bgpd.vty ]",
		"vtysh -c 'show bgp summary json'",
		"vtysh -c 'show bgp ipv4 unicast json'",
		"rm -f \"$SUMMARY\" \"$ROUTES\"",
	} {
		if !strings.Contains(reloadScript, expected) {
			t.Errorf("expected the reload loop to snapshot the bgp state, missing %q:\n%s", expected, reloadScript)
		}
	}
	if !strings.Contains(reloadScript, bgpSummaryFileName) || !strings.Contains(reloadScript, bgpRoutesFileName) {
		t.Errorf("expected the snapshot files the agent reads:\n%s", reloadScript)
	}
}
