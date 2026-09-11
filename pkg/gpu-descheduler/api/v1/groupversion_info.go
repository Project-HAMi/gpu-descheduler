// pkg/webhook/gpu-descheduler/api/v1/groupversion_info.go

// +kubebuilder:object:generate=true
// +groupName=scheduling.hami.placement.com

package v1

import (
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/scheme"
)

var (
	// GroupVersion is the group version information of this API group
	GroupVersion = schema.GroupVersion{
		Group:   "scheduling.hami.placement.com",
		Version: "v1",
	}

	// SchemeBuilder is used to register types into Scheme
	SchemeBuilder = &scheme.Builder{GroupVersion: GroupVersion}

	// AddToScheme adds all types of this API group to the specified Scheme
	AddToScheme = SchemeBuilder.AddToScheme
)
