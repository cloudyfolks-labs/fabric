package frr

import (
	"strings"
	"testing"
)

func TestReloadScriptPrefersReload(t *testing.T) {
	if !strings.Contains(reloadScript, "--reload --overwrite") {
		t.Errorf("expected the desired config to be applied by reload:\n%s", reloadScript)
	}
	reloadIdx := strings.Index(reloadScript, "--reload --overwrite")
	restartIdx := strings.Index(reloadScript, "exit 0")
	if restartIdx < reloadIdx {
		t.Errorf("the restart must be the fallback after a failed reload, not the first choice:\n%s", reloadScript)
	}
	if !strings.Contains(reloadScript, "restart") {
		t.Errorf("a failed reload of a changed vrf instance set must fall back to a restart:\n%s", reloadScript)
	}
}
