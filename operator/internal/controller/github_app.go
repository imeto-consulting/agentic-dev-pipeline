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
	"fmt"
	"net/http"
	"sync"

	"github.com/bradleyfalzon/ghinstallation/v2"
)

// GitHub App installation-token minting. When a pipeline-app-key Secret is
// present, the operator authenticates as a GitHub App installation instead of a
// long-lived PAT. Installation tokens TTL at ~1 hour, so a token leaked from an
// agent pod expires quickly and is scoped to a single installation — a far
// smaller blast radius than a fine-grained PAT.
//
// ghinstallation.Transport caches the installation token internally and
// transparently refreshes it near expiry, so calling Token() on every reconcile
// does NOT hit the JWT-mint endpoint each time. We cache one Transport per
// (appID, installationID) tuple to preserve that internal cache across calls.

var (
	ghAppTransports = map[string]*ghinstallation.Transport{}
	ghAppMu         sync.Mutex
)

// installationToken returns a fresh (cached, auto-refreshed) installation token.
func installationToken(ctx context.Context, appID, installationID int64, privateKeyPEM []byte) (string, error) {
	key := fmt.Sprintf("%d/%d", appID, installationID)

	ghAppMu.Lock()
	tr, ok := ghAppTransports[key]
	if !ok {
		newTr, err := ghinstallation.New(http.DefaultTransport, appID, installationID, privateKeyPEM)
		if err != nil {
			ghAppMu.Unlock()
			return "", fmt.Errorf("init github app transport: %w", err)
		}
		tr = newTr
		ghAppTransports[key] = tr
	}
	ghAppMu.Unlock()

	return tr.Token(ctx)
}
