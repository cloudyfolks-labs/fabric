package v1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type DNSZoneList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata"`

	Items []DNSZone `json:"items"`
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
type DNSZone struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata"`

	Spec   DNSZoneSpec   `json:"spec"`
	Status DNSZoneStatus `json:"status,omitempty"`
}

type DNSZoneSpec struct {
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Vpc string `json:"vpc"`
	// +optional
	Records []DNSZoneRecord `json:"records,omitempty"`
}

type DNSZoneRecord struct {
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinItems=1
	IPs []string `json:"ips"`
}

type DNSZoneStatus struct {
	// +optional
	ActiveRecords int32 `json:"activeRecords,omitempty"`
	// +optional
	// +patchStrategy=merge
	// +patchMergeKey=type
	Conditions []Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`
}
