/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// DevTaskSpec defines the desired state of DevTask
type DevTaskSpec struct {
	// IssueNumber is the GitHub issue number to implement
	// +kubebuilder:validation:Minimum=1
	IssueNumber int `json:"issueNumber"`

	// Repo is the GitHub repository in "owner/name" format
	// +kubebuilder:validation:Pattern=`^[a-zA-Z0-9_.-]+/[a-zA-Z0-9_.-]+$`
	Repo string `json:"repo"`
}

// DevTaskPhase represents the current lifecycle phase
// +kubebuilder:validation:Enum=Pending;Building;Running;AwaitingReview;BlockedOnClarification;Failed;Completed
type DevTaskPhase string

const (
	PhasePending                DevTaskPhase = "Pending"
	PhaseBuilding               DevTaskPhase = "Building"
	PhaseRunning                DevTaskPhase = "Running"
	PhaseAwaitingReview         DevTaskPhase = "AwaitingReview"
	PhaseBlockedOnClarification DevTaskPhase = "BlockedOnClarification"
	PhaseFailed                 DevTaskPhase = "Failed"
	PhaseCompleted              DevTaskPhase = "Completed"
)

// DevTaskStatus defines the observed state of DevTask.
type DevTaskStatus struct {
	// +optional
	Phase DevTaskPhase `json:"phase,omitempty"`
	// +optional
	Namespace string `json:"namespace,omitempty"`
	// +optional
	PRNumber int `json:"prNumber,omitempty"`
	// +optional
	StartedAt *metav1.Time `json:"startedAt,omitempty"`
	// +optional
	Message string `json:"message,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Phase",type="string",JSONPath=".status.phase"
// +kubebuilder:printcolumn:name="Issue",type="integer",JSONPath=".spec.issueNumber"
// +kubebuilder:printcolumn:name="PR",type="integer",JSONPath=".status.prNumber"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// DevTask is the Schema for the devtasks API
type DevTask struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of DevTask
	// +required
	Spec DevTaskSpec `json:"spec"`

	// status defines the observed state of DevTask
	// +optional
	Status DevTaskStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// DevTaskList contains a list of DevTask
type DevTaskList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []DevTask `json:"items"`
}

func init() {
	SchemeBuilder.Register(&DevTask{}, &DevTaskList{})
}
