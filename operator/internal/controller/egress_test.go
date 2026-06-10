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

import "testing"

func TestEgressRulesDefault(t *testing.T) {
	t.Setenv(egressProxyURLEnv, "")
	rules := egressRules()
	if len(rules) != 2 {
		t.Fatalf("want 2 egress rules (dns + all-443), got %d", len(rules))
	}
	// Second rule is port-only (no To peers) → all hosts on :443.
	if len(rules[1].To) != 0 {
		t.Errorf("default mode should allow all hosts on :443 (no To peers), got %d peers", len(rules[1].To))
	}
	if got := rules[1].Ports[0].Port.IntValue(); got != 443 {
		t.Errorf("default mode HTTPS port = %d, want 443", got)
	}
}

func TestEgressRulesProxyMode(t *testing.T) {
	t.Setenv(egressProxyURLEnv, "http://egress-proxy.egress-proxy.svc.cluster.local:3128")
	t.Setenv(egressProxyNamespaceEnv, "egress-proxy")
	rules := egressRules()
	if len(rules) != 2 {
		t.Fatalf("want 2 egress rules (dns + proxy), got %d", len(rules))
	}
	proxyRule := rules[1]
	if len(proxyRule.To) != 1 || proxyRule.To[0].NamespaceSelector == nil {
		t.Fatalf("proxy mode should restrict egress to the proxy namespace, got %+v", proxyRule.To)
	}
	if ns := proxyRule.To[0].NamespaceSelector.MatchLabels["kubernetes.io/metadata.name"]; ns != "egress-proxy" {
		t.Errorf("proxy peer namespace = %q, want egress-proxy", ns)
	}
	if got := proxyRule.Ports[0].Port.IntValue(); got != defaultEgressProxyPort {
		t.Errorf("proxy port = %d, want %d", got, defaultEgressProxyPort)
	}
}
