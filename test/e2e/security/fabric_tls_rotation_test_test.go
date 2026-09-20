package security

import (
	"slices"
	"testing"
)

func TestRestoreFabricTLSStateWaitsForStability(t *testing.T) {
	t.Parallel()

	var steps []string
	restoreFabricTLSState(
		func() { steps = append(steps, "restore") },
		func() { steps = append(steps, "projection") },
		func() { steps = append(steps, "ovn-central") },
		func() { steps = append(steps, "ovs-ovn") },
		func() { steps = append(steps, "connectivity") },
	)

	want := []string{"restore", "projection", "ovn-central", "ovs-ovn", "connectivity"}
	if !slices.Equal(steps, want) {
		t.Fatalf("unexpected TLS restoration steps: got %v, want %v", steps, want)
	}
}
