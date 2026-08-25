package controller

import (
	"testing"

	netv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"k8s.io/utils/set"

	"github.com/stretchr/testify/require"

	"github.com/cloudyfolks-labs/fabric/pkg/util"
)

func TestParsePolicyFor(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		annotation    *string
		wantProviders set.Set[string]
	}{
		{
			name:          "annotation omitted",
			annotation:    nil,
			wantProviders: nil,
		},
		{
			name:       "fabric only",
			annotation: new("fabric"),
			wantProviders: set.New(
				util.OvnProvider,
			),
		},
		{
			name:       "duplicate fabric",
			annotation: new("fabric, fabric"),
			wantProviders: set.New(
				util.OvnProvider,
			),
		},
		{
			name:       "secondary only",
			annotation: new("ns1/net1"),
			wantProviders: set.New(
				"net1.ns1." + util.OvnProvider,
			),
		},
		{
			name:       "fabric and secondary",
			annotation: new(" fabric , ns1/net1 "),
			wantProviders: set.New(
				util.OvnProvider,
				"net1.ns1."+util.OvnProvider,
			),
		},
		{
			name:       "fabric and invalid",
			annotation: new("fabric, foo"),
			wantProviders: set.New(
				util.OvnProvider,
			),
		},
		{
			name:          "invalid all",
			annotation:    new("all"),
			wantProviders: set.New[string](),
		},
		{
			name:          "invalid default",
			annotation:    new("default"),
			wantProviders: set.New[string](),
		},
		{
			name:          "invalid no entries",
			annotation:    new(","),
			wantProviders: set.New[string](),
		},
		{
			name:          "invalid token",
			annotation:    new("foo"),
			wantProviders: set.New[string](),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			np := &netv1.NetworkPolicy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "np",
					Namespace: "default",
				},
			}
			if tt.annotation != nil {
				np.Annotations = map[string]string{
					util.NetworkPolicyForAnnotation: *tt.annotation,
				}
			}

			providers := parsePolicyFor(np)
			if tt.wantProviders == nil {
				require.Nil(t, providers)
				return
			}
			require.Equal(t, tt.wantProviders, providers)
		})
	}
}

func TestNetpolAppliesToProvider(t *testing.T) {
	t.Parallel()
	providers := set.New("fabric", "net1.ns1.fabric")
	require.True(t, netpolAppliesToProvider("fabric", providers))
	require.False(t, netpolAppliesToProvider("net2.ns2.fabric", providers))
	require.True(t, netpolAppliesToProvider("fabric", nil))
	require.False(t, netpolAppliesToProvider("fabric", set.New[string]()))
}
