package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// SubgraphSchemaSpec defines the desired state of a federation subgraph.
type SubgraphSchemaSpec struct {
	// RoutingUrl is the URL where the subgraph can be reached by the router.
	// +kubebuilder:validation:Required
	RoutingUrl string `json:"routingUrl"`

	// Schema is the full GraphQL SDL for this subgraph.
	// +kubebuilder:validation:Required
	Schema string `json:"schema"`
}

// CompositionStatus represents the result of a supergraph composition.
// +kubebuilder:validation:Enum=Success;Failed;Pending
type CompositionStatus string

const (
	CompositionStatusSuccess CompositionStatus = "Success"
	CompositionStatusFailed  CompositionStatus = "Failed"
	CompositionStatusPending CompositionStatus = "Pending"
)

// SubgraphSchemaStatus defines the observed state after composition.
type SubgraphSchemaStatus struct {
	// CompositionStatus indicates whether the last composition succeeded.
	CompositionStatus CompositionStatus `json:"compositionStatus,omitempty"`

	// LastComposed is the timestamp of the last successful composition.
	LastComposed *metav1.Time `json:"lastComposed,omitempty"`

	// SupergraphChecksum is the SHA-256 of the last composed supergraph.
	SupergraphChecksum string `json:"supergraphChecksum,omitempty"`

	// Message provides human-readable details about the composition result.
	Message string `json:"message,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="URL",type=string,JSONPath=`.spec.routingUrl`
// +kubebuilder:printcolumn:name="Status",type=string,JSONPath=`.status.compositionStatus`
// +kubebuilder:printcolumn:name="Last Composed",type=date,JSONPath=`.status.lastComposed`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// SubgraphSchema is the Schema for the subgraphschemas API.
type SubgraphSchema struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   SubgraphSchemaSpec   `json:"spec,omitempty"`
	Status SubgraphSchemaStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// SubgraphSchemaList contains a list of SubgraphSchema.
type SubgraphSchemaList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []SubgraphSchema `json:"items"`
}

func init() {
	SchemeBuilder.Register(&SubgraphSchema{}, &SubgraphSchemaList{})
}
