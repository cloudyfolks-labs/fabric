package v1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type BgpConfList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata"`

	Items []BgpConf `json:"items"`
}

// +genclient
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +genclient:nonNamespaced
// +resourceName=bgp-confs
// +kubebuilder:resource:scope="Cluster",path="bgp-confs",singular="bgp-conf"
// +kubebuilder:subresource:status
// BgpConf configures the per-node FRR agent. IPv6 is not supported:
// neighbour addresses, the router id and the advertise filter must all
// be IPv4, and the agent rejects the configuration otherwise.
type BgpConf struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata"`

	Spec   BgpConfSpec   `json:"spec"`
	Status BgpConfStatus `json:"status,omitempty"`
}

// BgpConfStatus reports what every FRR agent that this configuration
// selects did with it.
type BgpConfStatus struct {
	// +optional
	// +listType=map
	// +listMapKey=node
	Nodes []BgpNodeStatus `json:"nodes,omitempty"`
}

const (
	BgpNodeStateApplied = "Applied"
	BgpNodeStatePending = "Pending"
	BgpNodeStateFailed  = "Failed"
)

type BgpNodeStatus struct {
	// +kubebuilder:validation:Required
	Node string `json:"node"`
	// Serial of the configuration the agent last rendered.
	// +optional
	Serial string `json:"serial,omitempty"`
	// +kubebuilder:validation:Enum=Applied;Pending;Failed
	// +optional
	State string `json:"state,omitempty"`
	// +optional
	Message string `json:"message,omitempty"`
	// +optional
	LastUpdateTime metav1.Time `json:"lastUpdateTime,omitempty"`
}

type BgpConfSpec struct {
	// +kubebuilder:validation:Format=int64
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=4294967295
	LocalASN uint32 `json:"localASN"`
	// +kubebuilder:validation:Format=int64
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=4294967295
	PeerASN       uint32          `json:"peerASN"`
	RouterID      string          `json:"routerId,omitempty"`
	Neighbours    []string        `json:"neighbours"`
	Password      string          `json:"password,omitempty"` //nolint:gosec // BGP password is runtime configuration, not a hardcoded secret.
	HoldTime      metav1.Duration `json:"holdTime,omitempty"`
	KeepaliveTime metav1.Duration `json:"keepaliveTime,omitempty"`
	ConnectTime   metav1.Duration `json:"connectTime,omitempty"`
	EbgpMultiHop  bool            `json:"ebgpMultiHop,omitempty"`

	GracefulRestart bool `json:"gracefulRestart,omitempty"`

	NodeSelector    map[string]string `json:"nodeSelector,omitempty"`
	Peers           []BgpPeer         `json:"peers,omitempty"`
	AdvertiseFilter []string          `json:"advertiseFilter,omitempty"`

	// +optional
	// +kubebuilder:validation:items:Minimum=1
	// +kubebuilder:validation:items:Maximum=65535
	RedistributeTables []uint32 `json:"redistributeTables,omitempty"`
}

type BgpPeer struct {
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Address string `json:"address"`
	// +kubebuilder:validation:Format=int64
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=4294967295
	ASN uint32 `json:"asn,omitempty"`
	BFD bool   `json:"bfd,omitempty"`
}
