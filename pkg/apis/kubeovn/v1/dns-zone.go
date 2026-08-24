package v1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type DnsZoneList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata"`

	Items []DnsZone `json:"items"`
}

// +genclient
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +genclient:nonNamespaced
// +resourceName=dns-zones
// +kubebuilder:resource:scope="Cluster",path="dns-zones",singular="dns-zone"
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Vpc",type=string,JSONPath=`.spec.vpc`
// +kubebuilder:printcolumn:name="Records",type=integer,JSONPath=`.status.activeRecords`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
type DnsZone struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata"`

	Spec   DnsZoneSpec   `json:"spec"`
	Status DnsZoneStatus `json:"status,omitempty"`
}

type DnsZoneSpec struct {
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Vpc string `json:"vpc"`
	// +optional
	Records []DnsZoneRecord `json:"records,omitempty"`
}

type DnsZoneRecord struct {
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinItems=1
	IPs []string `json:"ips"`
}

type DnsZoneStatus struct {
	// +optional
	ActiveRecords int32 `json:"activeRecords,omitempty"`
	// +optional
	// +patchStrategy=merge
	// +patchMergeKey=type
	Conditions []Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`
}
