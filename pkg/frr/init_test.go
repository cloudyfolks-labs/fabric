package frr

import (
	"strings"
	"testing"
)

func TestReloadScriptNeverExits(t *testing.T) {
	if strings.Contains(reloadScript, "exit 0") {
		t.Errorf("the reload loop is PID 1 of the FRR container, exiting bounces every BGP session:\n%s", reloadScript)
	}
	if !strings.Contains(reloadScript, "--reload --overwrite") {
		t.Errorf("expected the desired config to be applied by reload:\n%s", reloadScript)
	}
}
