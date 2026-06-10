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

import "os"

// Egress-proxy mode. By default the agent sandbox is allowed to reach any host
// on :443 (port-scoped egress). That lets a prompt-injected agent exfiltrate a
// token to an arbitrary host. When EGRESS_PROXY_URL is set, the operator
// instead:
//
//   - tightens the task NetworkPolicy to allow egress only to DNS + the proxy
//     (no direct :443), and
//   - injects HTTPS_PROXY / HTTP_PROXY / NO_PROXY into the agent containers,
//
// so all outbound HTTPS goes through the forward proxy, which enforces a
// CONNECT-domain allowlist (see deploy/egress-proxy/). This is opt-in because a
// misconfigured allowlist fails every task; default-off keeps existing setups
// working unchanged.

const (
	egressProxyURLEnv       = "EGRESS_PROXY_URL"       // e.g. http://egress-proxy.egress-proxy.svc.cluster.local:3128
	egressProxyNamespaceEnv = "EGRESS_PROXY_NAMESPACE" // namespace the proxy runs in (NetworkPolicy peer)
	defaultEgressProxyNS    = "egress-proxy"
	egressProxyPortEnv      = "EGRESS_PROXY_PORT"
	defaultEgressProxyPort  = 3128
)

func egressProxyURL() string { return os.Getenv(egressProxyURLEnv) }

func egressProxyEnabled() bool { return egressProxyURL() != "" }

func egressProxyNamespace() string {
	if v := os.Getenv(egressProxyNamespaceEnv); v != "" {
		return v
	}
	return defaultEgressProxyNS
}

func egressProxyPort() int {
	if v := os.Getenv(egressProxyPortEnv); v != "" {
		if n := atoiOrZero(v); n > 0 {
			return n
		}
	}
	return defaultEgressProxyPort
}

func atoiOrZero(s string) int {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0
		}
		n = n*10 + int(r-'0')
	}
	return n
}
