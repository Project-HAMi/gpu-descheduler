// pkg/webhook/gpu-descheduler/api/v1/placement_types.go
package v1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName={"vcp"}
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Node",type=string,JSONPath=`.spec.nodeName`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// HamiPodPlacement is the main type of the CRD
type HamiPodPlacement struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   HamiPodPlacementSpec   `json:"spec,omitempty"`
	Status HamiPodPlacementStatus `json:"status,omitempty"`
}

// HamiPodPlacementSpec is the specification part
type HamiPodPlacementSpec struct {
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=5
	ReqID string `json:"reqID"`

	// +kubebuilder:validation:Required
	PodSelector metav1.LabelSelector `json:"podSelector"`

	// +kubebuilder:validation:Required
	NodeName string `json:"nodeName"`

	// +kubebuilder:validation:MinItems=1
	GPUDevices []string `json:"gpuDevices"`

	// +kubebuilder:validation:Optional
	EvictPod string `json:"evictPod,omitempty"`
}

// AssignmentDetail records the allocation status for each Pod
type AssignmentDetail struct {
	PodName   string `json:"podName"`
	GPUDevice string `json:"gpuDevice"`
}

// HamiPodPlacementStatus is the status part
type HamiPodPlacementStatus struct {
	Phase             string             `json:"phase,omitempty"`
	AssignmentDetails []AssignmentDetail `json:"assignmentDetails,omitempty"`
	ConsumedAt        *metav1.Time       `json:"consumedAt,omitempty"`
	Message           string             `json:"message,omitempty"`
	MatchedPodName    string             `json:"matchedPodName,omitempty"`
}

// +kubebuilder:object:root=true

// HamiPodPlacementList is the list type
type HamiPodPlacementList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []HamiPodPlacement `json:"items"`
}

func init() {
	// Register types with SchemeBuilder
	SchemeBuilder.Register(&HamiPodPlacement{}, &HamiPodPlacementList{})
}
