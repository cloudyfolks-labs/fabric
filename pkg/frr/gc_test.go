package frr

import (
	"reflect"
	"testing"
)

func TestPlanRoutePurge(t *testing.T) {
	owned := map[uint32]struct{}{4: {}, 198: {}}
	routes := []tableRoute{
		{Table: 9, Protocol: ovnRouteProtocol, Dst: "100.67.0.3/32"},
		{Table: 4, Protocol: ovnRouteProtocol, Dst: "100.67.0.3/32"},
		{Table: 4, Protocol: ovnRouteProtocol, Dst: "100.67.0.4/32"},
		{Table: 9, Protocol: 3, Dst: "100.67.0.5/32"},
		{Table: 198, Protocol: 248, Dst: "91.246.31.2/32"},
		{Table: 254, Protocol: ovnRouteProtocol, Dst: "10.0.0.0/24"},
		{Table: 0, Protocol: ovnRouteProtocol, Dst: "10.0.1.0/24"},
	}
	plan := planRoutePurge(routes, owned)
	wantStale := []tableRoute{{Table: 9, Protocol: ovnRouteProtocol, Dst: "100.67.0.3/32"}}
	if !reflect.DeepEqual(plan.Stale, wantStale) {
		t.Errorf("stale plan differs: %v", plan.Stale)
	}
	wantReassert := []tableRoute{{Table: 4, Protocol: ovnRouteProtocol, Dst: "100.67.0.3/32"}}
	if !reflect.DeepEqual(plan.Reassert, wantReassert) {
		t.Errorf("reassert plan differs: %v", plan.Reassert)
	}
}

func TestPlanRoutePurgeKeepsOwnedAndForeign(t *testing.T) {
	owned := map[uint32]struct{}{7: {}}
	routes := []tableRoute{
		{Table: 7, Protocol: ovnRouteProtocol, Dst: "100.67.0.7/32"},
		{Table: 11, Protocol: 2, Dst: "192.0.2.0/24"},
		{Table: 253, Protocol: ovnRouteProtocol, Dst: "192.0.2.1/32"},
	}
	plan := planRoutePurge(routes, owned)
	if len(plan.Stale) != 0 || len(plan.Reassert) != 0 {
		t.Errorf("expected an empty plan, got %+v", plan)
	}
}
