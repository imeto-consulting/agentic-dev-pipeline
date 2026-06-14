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
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var _ = Describe("Triage token refresher", func() {
	const tns = defaultTriageNamespace

	ensureNs := func(name string) {
		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}
		if err := k8sClient.Create(ctx, ns); err != nil && !apierrors.IsAlreadyExists(err) {
			Expect(err).NotTo(HaveOccurred())
		}
	}

	BeforeEach(func() {
		ensureNs(tns)
	})

	AfterEach(func() {
		_ = k8sClient.Delete(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "pipeline-creds", Namespace: tns},
		})
	})

	It("updates only github-token and preserves the Claude creds", func() {
		seed := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "pipeline-creds", Namespace: tns},
			StringData: map[string]string{
				"github-token":     "old-pat",
				"claude-token":     "keep-me",
				"claude-auth-mode": "oauth",
			},
		}
		Expect(k8sClient.Create(ctx, seed)).To(Succeed())

		Expect(upsertTriageGithubToken(ctx, k8sClient, "ghs_minted_fresh")).To(Succeed())

		got := &corev1.Secret{}
		Expect(k8sClient.Get(ctx, client.ObjectKey{Namespace: tns, Name: "pipeline-creds"}, got)).To(Succeed())
		Expect(string(got.Data["github-token"])).To(Equal("ghs_minted_fresh"))
		Expect(string(got.Data["claude-token"])).To(Equal("keep-me"))
		Expect(string(got.Data["claude-auth-mode"])).To(Equal("oauth"))
	})

	It("is a no-op (no error, no Secret created) when the triage Secret is absent", func() {
		Expect(upsertTriageGithubToken(ctx, k8sClient, "ghs_minted_fresh")).To(Succeed())

		got := &corev1.Secret{}
		err := k8sClient.Get(ctx, client.ObjectKey{Namespace: tns, Name: "pipeline-creds"}, got)
		Expect(apierrors.IsNotFound(err)).To(BeTrue())
	})
})
