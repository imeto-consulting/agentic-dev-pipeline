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

package controller

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	devpipelinev1alpha1 "github.com/jonaseck2/agentic-dev-pipeline/operator/api/v1alpha1"
)

// ensureNetworkPolicy creates the deny-all + allowlist egress policy for the task namespace.
// Allows: kube-dns (UDP/TCP 53), all external HTTPS (:443).
// Calico enforces this — Flannel does not, so the cluster must use Calico CNI.
func ensureNetworkPolicy(ctx context.Context, c client.Client, task *devpipelinev1alpha1.DevTask) error {
	tcp := corev1.ProtocolTCP
	udp := corev1.ProtocolUDP
	port53 := intstr.FromInt(53)
	port443 := intstr.FromInt(443)

	policy := &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "sandbox-egress",
			Namespace: taskNamespace(task),
		},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{},
			PolicyTypes: []networkingv1.PolicyType{
				networkingv1.PolicyTypeIngress,
				networkingv1.PolicyTypeEgress,
			},
			Ingress: []networkingv1.NetworkPolicyIngressRule{},
			Egress: []networkingv1.NetworkPolicyEgressRule{
				{
					Ports: []networkingv1.NetworkPolicyPort{
						{Protocol: &udp, Port: &port53},
						{Protocol: &tcp, Port: &port53},
					},
				},
				{
					Ports: []networkingv1.NetworkPolicyPort{
						{Protocol: &tcp, Port: &port443},
					},
				},
			},
		},
	}
	return client.IgnoreAlreadyExists(c.Create(ctx, policy))
}
