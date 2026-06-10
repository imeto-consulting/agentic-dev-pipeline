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

// ensureNetworkPolicy creates the deny-all-ingress + allowlist-egress policy for
// the task namespace. Calico enforces this — Flannel does not, so the cluster
// must use Calico CNI.
//
// Default egress: kube-dns (UDP/TCP 53) + all external HTTPS (:443).
// Egress-proxy mode (EGRESS_PROXY_URL set): kube-dns + the proxy only; the agent
// has no direct :443, so all outbound HTTPS is funneled through the forward
// proxy's CONNECT-domain allowlist.
func ensureNetworkPolicy(ctx context.Context, c client.Client, task *devpipelinev1alpha1.DevTask) error {
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
			Egress:  egressRules(),
		},
	}
	return client.IgnoreAlreadyExists(c.Create(ctx, policy))
}

// egressRules builds the egress allowlist for the sandbox: always DNS, then
// either the proxy-only rule (proxy mode) or all-:443 (default).
func egressRules() []networkingv1.NetworkPolicyEgressRule {
	tcp := corev1.ProtocolTCP
	udp := corev1.ProtocolUDP
	port53 := intstr.FromInt(53)

	dns := networkingv1.NetworkPolicyEgressRule{
		Ports: []networkingv1.NetworkPolicyPort{
			{Protocol: &udp, Port: &port53},
			{Protocol: &tcp, Port: &port53},
		},
	}

	if egressProxyEnabled() {
		proxyPort := intstr.FromInt(egressProxyPort())
		return []networkingv1.NetworkPolicyEgressRule{
			dns,
			{
				To: []networkingv1.NetworkPolicyPeer{{
					NamespaceSelector: &metav1.LabelSelector{
						MatchLabels: map[string]string{
							"kubernetes.io/metadata.name": egressProxyNamespace(),
						},
					},
				}},
				Ports: []networkingv1.NetworkPolicyPort{
					{Protocol: &tcp, Port: &proxyPort},
				},
			},
		}
	}

	port443 := intstr.FromInt(443)
	return []networkingv1.NetworkPolicyEgressRule{
		dns,
		{
			Ports: []networkingv1.NetworkPolicyPort{
				{Protocol: &tcp, Port: &port443},
			},
		},
	}
}
