package frr

import (
	"strings"
	"testing"
)

func TestReloadScriptRestartsOnVrfSetChange(t *testing.T) {
	if !strings.Contains(reloadScript, "--reload --overwrite") {
		t.Errorf("expected an unchanged vrf instance set to be applied by reload:\n%s", reloadScript)
	}
	reloadIdx := strings.Index(reloadScript, "--reload --overwrite")
	restartIdx := strings.Index(reloadScript, "exit 0")
	if reloadIdx < restartIdx {
		t.Errorf("a changed vrf instance set must restart before any reload attempt:\n%s", reloadScript)
	}
	if !strings.Contains(reloadScript, "restart") {
		t.Errorf("a changed vrf instance set must lead to a restart:\n%s", reloadScript)
	}
	if !strings.Contains(reloadScript, "| sort") {
		t.Errorf("the vrf instance sets must be compared as sorted sets, not counts:\n%s", reloadScript)
	}
}
