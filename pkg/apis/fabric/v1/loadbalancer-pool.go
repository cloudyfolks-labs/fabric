package v1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type LoadBalancerPoolList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata"`

	Items []LoadBalancerPool `json:"items"`
}

// +genclient
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +genclient:nonNamespaced
// +resourceName=loadbalancer-pools
// +kubebuilder:resource:scope="Cluster",path="loadbalancer-pools",singular="loadbalancer-pool"
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Subnet",type=string,JSONPath=`.spec.subnet`
// +kubebuilder:printcolumn:name="Announce",type=string,JSONPath=`.spec.announce`
// +kubebuilder:printcolumn:name="Default",type=boolean,JSONPath=`.spec.default`
// +kubebuilder:printcolumn:name="Available",type=integer,JSONPath=`.status.available`
// +kubebuilder:printcolumn:name="InUse",type=integer,JSONPath=`.status.inUse`
type LoadBalancerPool struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata"`

	Spec   LoadBalancerPoolSpec   `json:"spec"`
	Status LoadBalancerPoolStatus `json:"status,omitempty"`
}

type LoadBalancerPoolSpec struct {
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Subnet string `json:"subnet"`
	// +kubebuilder:validation:Enum=l2;bgp
	// +kubebuilder:default=l2
	Announce string `json:"announce,omitempty"`
	// +optional
	ServiceSelector *metav1.LabelSelector `json:"serviceSelector,omitempty"`
	// +optional
	Default bool `json:"default,omitempty"`
}

type LoadBalancerPoolStatus struct {
	// +optional
	Available int64 `json:"available,omitempty"`
	// +optional
	InUse int64 `json:"inUse,omitempty"`
	// +optional
	// +patchStrategy=merge
	// +patchMergeKey=type
	Conditions []Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`
}

const (
	LoadBalancerPoolAnnounceL2  = "l2"
	LoadBalancerPoolAnnounceBGP = "bgp"
)
