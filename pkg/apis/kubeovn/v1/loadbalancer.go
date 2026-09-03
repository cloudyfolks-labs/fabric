package v1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type LoadBalancerList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata"`

	Items []LoadBalancer `json:"items"`
}

// +genclient
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +genclient:nonNamespaced
// +resourceName=loadbalancers
// +kubebuilder:resource:scope="Cluster",shortName="flb",path="loadbalancers",singular="loadbalancer"
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="vpc",type="string",JSONPath=".spec.vpc"
// +kubebuilder:printcolumn:name="vip",type="string",JSONPath=".status.vip"
// +kubebuilder:printcolumn:name="port(s)",type="string",JSONPath=".status.ports"
// +kubebuilder:printcolumn:name="service",type="string",JSONPath=".status.service"
// +kubebuilder:printcolumn:name="age",type="date",JSONPath=".metadata.creationTimestamp"

// LoadBalancer is the one L4 balancer object: the frontend field picks
// the OVN realization. A literal vip attaches to the VPC's switches
// (the SwitchLBRule shape); an ovnEip attaches to the router and uses
// the EIP's public address as the VIP (the RouterLBRule shape).
// Exactly one frontend field must be set.
type LoadBalancer struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata"`

	Spec   LoadBalancerSpec   `json:"spec"`
	Status LoadBalancerStatus `json:"status"`
}

type LoadBalancerSpec struct {
	Vpc             string               `json:"vpc"`
	Frontend        LoadBalancerFrontend `json:"frontend"`
	Namespace       string               `json:"namespace"`
	Selector        []string             `json:"selector"`
	Endpoints       []string             `json:"endpoints"`
	SessionAffinity string               `json:"sessionAffinity,omitempty"`
	Ports           []LoadBalancerPort   `json:"ports"`
}

type LoadBalancerFrontend struct {
	// +optional
	Vip string `json:"vip,omitempty"`
	// +optional
	OvnEip string `json:"ovnEip,omitempty"`
}

type LoadBalancerPort struct {
	Name       string `json:"name"`
	Port       int32  `json:"port"`
	TargetPort int32  `json:"targetPort,omitempty"`
	Protocol   string `json:"protocol"`
}

type LoadBalancerStatus struct {
	// +optional
	// +patchMergeKey=type
	// +patchStrategy=merge
	Conditions []Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`

	Vip     string `json:"vip" patchStrategy:"merge"`
	Ports   string `json:"ports" patchStrategy:"merge"`
	Service string `json:"service" patchStrategy:"merge"`
}
