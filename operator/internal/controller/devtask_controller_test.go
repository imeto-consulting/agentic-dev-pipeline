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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	devpipelinev1alpha1 "github.com/jonaseck2/agentic-dev-pipeline/operator/api/v1alpha1"
)

var _ = Describe("DevTask Controller", func() {
	Context("When reconciling a fresh resource", func() {
		const (
			resourceName = "test-resource-42"
			issueNumber  = 42
		)

		ctx := context.Background()

		typeNamespacedName := types.NamespacedName{
			Name:      resourceName,
			Namespace: systemNamespace,
		}

		ensureNamespaceExists := func(name string) {
			ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}
			err := k8sClient.Create(ctx, ns)
			if err != nil && !errors.IsAlreadyExists(err) {
				Expect(err).NotTo(HaveOccurred())
			}
		}

		BeforeEach(func() {
			By("seeding the system namespace and pipeline-creds secret")
			ensureNamespaceExists(systemNamespace)
			creds := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: "pipeline-creds", Namespace: systemNamespace},
				StringData: map[string]string{
					"github-token":     "test-token",
					"claude-token":     "test-claude",
					"claude-auth-mode": "api",
					"git-author-name":  "Test Bot",
					"git-author-email": "bot@example.com",
				},
			}
			err := k8sClient.Create(ctx, creds)
			if err != nil && !errors.IsAlreadyExists(err) {
				Expect(err).NotTo(HaveOccurred())
			}

			By("creating a valid DevTask")
			existing := &devpipelinev1alpha1.DevTask{}
			if err := k8sClient.Get(ctx, typeNamespacedName, existing); err != nil && errors.IsNotFound(err) {
				resource := &devpipelinev1alpha1.DevTask{
					ObjectMeta: metav1.ObjectMeta{Name: resourceName, Namespace: systemNamespace},
					Spec: devpipelinev1alpha1.DevTaskSpec{
						IssueNumber: issueNumber,
						Repo:        "owner/repo",
					},
				}
				Expect(k8sClient.Create(ctx, resource)).To(Succeed())
			}
		})

		AfterEach(func() {
			resource := &devpipelinev1alpha1.DevTask{}
			if err := k8sClient.Get(ctx, typeNamespacedName, resource); err == nil {
				Expect(k8sClient.Delete(ctx, resource)).To(Succeed())
			}
			_ = k8sClient.Delete(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "devtask-42"}})
		})

		It("provisions the task sandbox and advances to Building", func() {
			controllerReconciler := &DevTaskReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			By("reconciling the fresh resource")
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			By("creating the per-task namespace")
			taskNs := &corev1.Namespace{}
			Expect(k8sClient.Get(ctx, client.ObjectKey{Name: "devtask-42"}, taskNs)).To(Succeed())

			By("advancing the DevTask to Building with a recorded namespace")
			updated := &devpipelinev1alpha1.DevTask{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, updated)).To(Succeed())
			Expect(updated.Status.Phase).To(Equal(devpipelinev1alpha1.PhaseBuilding))
			Expect(updated.Status.Namespace).To(Equal("devtask-42"))
		})
	})
})
