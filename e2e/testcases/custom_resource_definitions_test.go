// Copyright 2022 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package e2e

import (
	"context"
	"fmt"
	"io/ioutil"
	"path/filepath"
	"testing"
	"time"

	"github.com/pkg/errors"
	admissionv1 "k8s.io/api/admissionregistration/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"kpt.dev/configsync/e2e/nomostest"
	"kpt.dev/configsync/e2e/nomostest/metrics"
	nomostesting "kpt.dev/configsync/e2e/nomostest/testing"
	"kpt.dev/configsync/pkg/api/configsync"
	"kpt.dev/configsync/pkg/importer/analyzer/validation/nonhierarchical"
	"kpt.dev/configsync/pkg/kinds"
	"kpt.dev/configsync/pkg/testing/fake"
	"kpt.dev/configsync/pkg/webhook/configuration"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func mustRemoveCustomResourceWithDefinition(nt *nomostest.NT, crd client.Object) {
	nt.RootRepos[configsync.RootSyncName].Add("acme/cluster/anvil-crd.yaml", crd)
	nt.RootRepos[configsync.RootSyncName].Add("acme/namespaces/foo/ns.yaml", fake.NamespaceObject("foo"))
	nt.RootRepos[configsync.RootSyncName].Add("acme/namespaces/foo/anvil-v1.yaml", anvilCR("v1", "heavy", 10))
	nt.RootRepos[configsync.RootSyncName].CommitAndPush("Adding Anvil CRD and one Anvil CR")
	nt.WaitForRepoSyncs()
	nt.RenewClient()

	err := nt.Validate(configuration.Name, "", &admissionv1.ValidatingWebhookConfiguration{},
		hasRule("acme.com.v1.admission-webhook.configsync.gke.io"))
	if err != nil {
		nt.T.Fatal(err)
	}

	err = nt.Validate("heavy", "foo", anvilCR("v1", "", 0))
	if err != nil {
		nt.T.Fatal(err)
	}

	// Validate multi-repo metrics.
	// The "declared_resources" metric only includes the count of objects with
	// known resources. On the first apply pass, the Anvil CR will be excluded,
	// and the metric will be recorded with status=error. The second apply pass
	// should include the Anvil CR, but will be reported as the same commit. So
	// syncing to the latest commit isn't enough to ensure the metrics are up to
	// date. We also have to make sure status=success.
	// Unfortunately, this means failure will always time out.
	err = nt.ValidateMetrics(nomostest.SyncMetricsToLatestCommitSyncedWithSuccess(nt), func() error {
		return nt.ValidateMultiRepoMetrics(nomostest.DefaultRootReconcilerName,
			nt.DefaultRootSyncObjectCount()+3, // 3 for the test Namespace, CustomResourceDefinition & Anvil
			metrics.ResourceCreated("Namespace"),
			metrics.ResourceCreated("CustomResourceDefinition"),
			metrics.ResourceCreated("Anvil"))
	})
	if err != nil {
		nt.T.Error(err)
	}

	// This should cause an error.
	nt.RootRepos[configsync.RootSyncName].Remove("acme/cluster/anvil-crd.yaml")
	nt.RootRepos[configsync.RootSyncName].CommitAndPush("Removing Anvil CRD but leaving Anvil CR")

	nt.WaitForRootSyncSourceError(configsync.RootSyncName, nonhierarchical.UnsupportedCRDRemovalErrorCode, "")

	err = nt.ValidateMetrics(nomostest.SyncMetricsToReconcilerSourceError(nt, nomostest.DefaultRootReconcilerName), func() error {
		// Validate reconciler error metric is emitted.
		return nt.ValidateReconcilerErrors(nomostest.DefaultRootReconcilerName, 1, 0)
	})
	if err != nil {
		nt.T.Error(err)
	}

	// This should fix the error.
	nt.RootRepos[configsync.RootSyncName].Remove("acme/namespaces/foo/anvil-v1.yaml")
	nt.RootRepos[configsync.RootSyncName].CommitAndPush("Removing the Anvil CR as well")
	nt.WaitForRepoSyncs()

	// Validate reconciler error is cleared.
	// The applier can prune both the CR and CRD in the same pass. So for this
	// validation we can just wait for the commit to match, which will
	// allow faster failure on error.
	err = nt.ValidateMetrics(nomostest.SyncMetricsToLatestCommit(nt), func() error {
		err := nt.ValidateMultiRepoMetrics(nomostest.DefaultRootReconcilerName,
			nt.DefaultRootSyncObjectCount()+1, // 1 for the test Namespace
			metrics.ResourceDeleted("CustomResourceDefinition"),
			metrics.ResourceDeleted("Anvil"))
		if err != nil {
			return err
		}
		return nt.ValidateReconcilerErrors(nomostest.DefaultRootReconcilerName, 0, 0)
	})
	if err != nil {
		nt.T.Error(err)
	}
}
func TestMustRemoveCustomResourceWithDefinitionV1Beta1(t *testing.T) {
	nt := nomostest.New(t, nomostesting.Reconciliation1)
	support, err := nt.SupportV1Beta1CRDAndRBAC()
	if err != nil {
		nt.T.Fatal("failed to check the supported CRD versions")
	}
	if support {
		mustRemoveCustomResourceWithDefinition(nt, anvilV1Beta1CRD())
	}
}

func TestMustRemoveCustomResourceWithDefinitionV1(t *testing.T) {
	nt := nomostest.New(t, nomostesting.Reconciliation1)
	mustRemoveCustomResourceWithDefinition(nt, anvilV1CRD())
}

func addAndRemoveCustomResource(nt *nomostest.NT, dir string, crd string) {
	crdFile := filepath.Join(".", "..", "testdata", "customresources", dir, crd)
	crdContent, err := ioutil.ReadFile(crdFile)
	if err != nil {
		nt.T.Fatal(err)
	}
	nt.RootRepos[configsync.RootSyncName].AddFile("acme/cluster/anvil-crd.yaml", crdContent)
	nt.RootRepos[configsync.RootSyncName].Add("acme/namespaces/prod/ns.yaml", fake.NamespaceObject("prod"))
	nt.RootRepos[configsync.RootSyncName].Add("acme/namespaces/prod/anvil.yaml", anvilCR("v1", "e2e-test-anvil", 10))
	nt.RootRepos[configsync.RootSyncName].CommitAndPush("Adding Anvil CRD and one Anvil CR")
	nt.WaitForRepoSyncs()
	nt.RenewClient()

	err = nt.Validate("e2e-test-anvil", "prod", anvilCR("v1", "", 10))
	if err != nil {
		nt.T.Fatal(err)
	}

	// Validate multi-repo metrics.
	err = nt.ValidateMetrics(nomostest.SyncMetricsToLatestCommit(nt), func() error {
		err = nt.ValidateMultiRepoMetrics(nomostest.DefaultRootReconcilerName,
			nt.DefaultRootSyncObjectCount()+3, // 3 for the test Namespace, CustomResourceDefinition & Anvil
			metrics.ResourceCreated("Namespace"), metrics.ResourceCreated("CustomResourceDefinition"), metrics.ResourceCreated("Anvil"))
		if err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		nt.T.Errorf("validating metrics: %v", err)
	}

	// Remove the CustomResource.
	nt.RootRepos[configsync.RootSyncName].Remove("acme/namespaces/prod/anvil.yaml")
	nt.RootRepos[configsync.RootSyncName].CommitAndPush("Removing Anvil CR but leaving Anvil CRD")
	nt.WaitForRepoSyncs()
	err = nt.ValidateNotFound("e2e-test-anvil", "prod", anvilCR("v1", "", 10))
	if err != nil {
		nt.T.Fatal(err)
	}

	// Remove the CustomResourceDefinition.
	nt.RootRepos[configsync.RootSyncName].Remove("acme/cluster/anvil-crd.yaml")
	nt.RootRepos[configsync.RootSyncName].CommitAndPush("Removing the Anvil CRD as well")
	nt.WaitForRepoSyncs()
	err = nt.ValidateNotFound("anvils.acme.com", "", fake.CustomResourceDefinitionV1Object())
	if err != nil {
		nt.T.Fatal(err)
	}
}

func TestAddAndRemoveCustomResourceV1(t *testing.T) {
	nt := nomostest.New(t, nomostesting.Reconciliation1)
	addAndRemoveCustomResource(nt, "v1_crds", "anvil-crd.yaml")
}

func TestAddAndRemoveCustomResourceV1Beta1(t *testing.T) {
	nt := nomostest.New(t, nomostesting.Reconciliation1)
	support, err := nt.SupportV1Beta1CRDAndRBAC()
	if err != nil {
		nt.T.Fatal("failed to check the supported CRD versions")
	}
	if support {
		addAndRemoveCustomResource(nt, "v1beta1_crds", "anvil-crd-v1.yaml")
	}
}

func mustRemoveUnManagedCustomResource(nt *nomostest.NT, dir string, crd string) {
	crdFile := filepath.Join(".", "..", "testdata", "customresources", dir, crd)
	crdContent, err := ioutil.ReadFile(crdFile)
	if err != nil {
		nt.T.Fatal(err)
	}
	nt.RootRepos[configsync.RootSyncName].AddFile("acme/cluster/anvil-crd.yaml", crdContent)
	nt.RootRepos[configsync.RootSyncName].Add("acme/namespaces/prod/ns.yaml", fake.NamespaceObject("prod"))
	nt.RootRepos[configsync.RootSyncName].CommitAndPush("Adding Anvil CRD")
	nt.WaitForRepoSyncs()
	nt.RenewClient()

	// Validate multi-repo metrics.
	err = nt.ValidateMetrics(nomostest.SyncMetricsToLatestCommit(nt), func() error {
		err := nt.ValidateMultiRepoMetrics(nomostest.DefaultRootReconcilerName,
			nt.DefaultRootSyncObjectCount()+2, // 2 for the test Namespace & CustomResourceDefinition
			metrics.ResourceCreated("CustomResourceDefinition"),
			metrics.ResourceCreated("Namespace"))
		return err
	})
	if err != nil {
		nt.T.Error(err)
	}

	err = nt.Validate("anvils.acme.com", "", fake.CustomResourceDefinitionV1Object())
	if err != nil {
		nt.T.Fatal(err)
	}

	// Apply the CustomResource.
	cr := anvilCR("v1", "e2e-test-anvil", 100)
	cr.SetNamespace("prod")
	err = nt.Client.Create(context.TODO(), cr)
	if err != nil {
		nt.T.Fatal(err)
	}

	// Remove the CustomResourceDefinition.
	nt.RootRepos[configsync.RootSyncName].Remove("acme/cluster/anvil-crd.yaml")
	nt.RootRepos[configsync.RootSyncName].CommitAndPush("Removing the Anvil CRD")
	nt.WaitForRepoSyncs()

	err = nt.ValidateNotFound("anvils.acme.com", "", fake.CustomResourceDefinitionV1Object())
	if err != nil {
		nt.T.Fatal(err)
	}
}
func TestMustRemoveUnManagedCustomResourceV1(t *testing.T) {
	nt := nomostest.New(t, nomostesting.Reconciliation1)
	mustRemoveUnManagedCustomResource(nt, "v1_crds", "anvil-crd.yaml")
}

func TestMustRemoveUnManagedCustomResourceV1Beta1(t *testing.T) {
	nt := nomostest.New(t, nomostesting.Reconciliation1)
	support, err := nt.SupportV1Beta1CRDAndRBAC()
	if err != nil {
		nt.T.Fatal("failed to check the supported CRD versions")
	}
	if support {
		mustRemoveUnManagedCustomResource(nt, "v1beta1_crds", "anvil-crd-v1.yaml")
	}
}

func addUpdateRemoveClusterScopedCRD(nt *nomostest.NT, dir string, crd string) {
	crdFile := filepath.Join(".", "..", "testdata", "customresources", dir, crd)
	crdContent, err := ioutil.ReadFile(crdFile)
	if err != nil {
		nt.T.Fatal(err)
	}
	nt.RootRepos[configsync.RootSyncName].AddFile("acme/cluster/clusteranvil-crd.yaml", crdContent)
	nt.RootRepos[configsync.RootSyncName].Add("acme/cluster/clusteranvil.yaml", clusteranvilCR("v1", "e2e-test-clusteranvil", 10))
	nt.RootRepos[configsync.RootSyncName].CommitAndPush("Adding clusterscoped Anvil CRD and CR")
	nt.WaitForRepoSyncs()
	nt.RenewClient()

	err = nt.Validate("e2e-test-clusteranvil", "", clusteranvilCR("v1", "", 10))
	if err != nil {
		nt.T.Fatal(err)
	}

	// Validate multi-repo metrics.
	// The "declared_resources" metric only includes the count of objects with
	// known resources. On the first apply pass, the Anvil CR will be excluded,
	// and the metric will be recorded with status=error. The second apply pass
	// should include the Anvil CR, but will be reported as the same commit. So
	// syncing to the latest commit isn't enough to ensure the metrics are up to
	// date. We also have to make sure status=success.
	// Unfortunately, this means failure will always time out.
	err = nt.ValidateMetrics(nomostest.SyncMetricsToLatestCommitSyncedWithSuccess(nt), func() error {
		err := nt.ValidateMultiRepoMetrics(nomostest.DefaultRootReconcilerName,
			nt.DefaultRootSyncObjectCount()+2, // 2 for the test CustomResourceDefinition & Anvil
			metrics.ResourceCreated("CustomResourceDefinition"),
			metrics.ResourceCreated("ClusterAnvil"))
		if err != nil {
			return err
		}
		return nt.ValidateErrorMetricsNotFound()
	})
	if err != nil {
		nt.T.Error(err)
	}

	// Update the CRD from version v1 to version v2.
	crdFile = filepath.Join(".", "..", "testdata", "customresources", dir, "clusteranvil-crd-v2.yaml")
	crdContent, err = ioutil.ReadFile(crdFile)
	if err != nil {
		nt.T.Fatal(err)
	}
	nt.RootRepos[configsync.RootSyncName].AddFile("acme/cluster/clusteranvil-crd.yaml", crdContent)
	nt.RootRepos[configsync.RootSyncName].CommitAndPush("Updating the Anvil CRD")
	nt.WaitForRepoSyncs()

	err = nt.Validate("clusteranvils.acme.com", "", fake.CustomResourceDefinitionV1Object(), hasTwoVersions)
	if err != nil {
		nt.T.Error(err)
	}
	err = nt.Validate("e2e-test-clusteranvil", "", clusteranvilCR("v2", "", 10))
	if err != nil {
		nt.T.Error(err)
	}
	if nt.T.Failed() {
		nt.T.FailNow()
	}

	// Remove the CR and CRD.
	nt.RootRepos[configsync.RootSyncName].Remove("acme/cluster")
	// Add back the safety ClusterRole to pass the safety check (KNV2006).
	nt.RootRepos[configsync.RootSyncName].AddSafetyClusterRole()
	nt.RootRepos[configsync.RootSyncName].CommitAndPush("Removing the Anvil CRD as well")
	nt.WaitForRepoSyncs()
	err = nt.ValidateNotFoundOrNoMatch("e2e-test-clusteranvil", "prod", clusteranvilCR("v2", "", 10))
	if err != nil {
		nt.T.Error(err)
	}
	err = nt.ValidateNotFound("clusteranvils.acme.com", "", fake.CustomResourceDefinitionV1Object())
	if err != nil {
		nt.T.Error(err)
	}
	if nt.T.Failed() {
		nt.T.FailNow()
	}
}
func TestAddUpdateRemoveClusterScopedCRDV1(t *testing.T) {
	nt := nomostest.New(t, nomostesting.Reconciliation1)
	addUpdateRemoveClusterScopedCRD(nt, "v1_crds", "clusteranvil-crd.yaml")
}

func TestAddUpdateRemoveClusterScopedCRDV1Beta1(t *testing.T) {
	nt := nomostest.New(t, nomostesting.Reconciliation1)
	support, err := nt.SupportV1Beta1CRDAndRBAC()
	if err != nil {
		nt.T.Fatal("failed to check the supported CRD versions")
	}
	if support {
		addUpdateRemoveClusterScopedCRD(nt, "v1beta1_crds", "clusteranvil-crd-v1.yaml")
	}
}

func addUpdateNamespaceScopedCRD(nt *nomostest.NT, dir string, crd string) {
	crdFile := filepath.Join(".", "..", "testdata", "customresources", dir, crd)
	crdContent, err := ioutil.ReadFile(crdFile)
	if err != nil {
		nt.T.Fatal(err)
	}
	nt.RootRepos[configsync.RootSyncName].AddFile("acme/cluster/anvil-crd.yaml", crdContent)
	nt.RootRepos[configsync.RootSyncName].Add("acme/namespaces/prod/anvil.yaml", anvilCR("v1", "e2e-test-anvil", 10))
	nt.RootRepos[configsync.RootSyncName].Add("acme/namespaces/prod/ns.yaml", fake.NamespaceObject("prod"))
	nt.RootRepos[configsync.RootSyncName].CommitAndPush("Adding namespacescoped Anvil CRD and CR")
	nt.WaitForRepoSyncs()
	nt.RenewClient()

	err = nt.Validate("e2e-test-anvil", "prod", anvilCR("v1", "", 10))
	if err != nil {
		nt.T.Fatal(err)
	}

	// Validate multi-repo metrics.
	// The "declared_resources" metric only includes the count of objects with
	// known resources. On the first apply pass, the Anvil CR will be excluded,
	// and the metric will be recorded with status=error. The second apply pass
	// should include the Anvil CR, but will be reported as the same commit. So
	// syncing to the latest commit isn't enough to ensure the metrics are up to
	// date. We also have to make sure status=success.
	// Unfortunately, this means failure will always time out.
	err = nt.ValidateMetrics(nomostest.SyncMetricsToLatestCommitSyncedWithSuccess(nt), func() error {
		err := nt.ValidateMultiRepoMetrics(nomostest.DefaultRootReconcilerName,
			nt.DefaultRootSyncObjectCount()+3, // 3 for the test Namespace, CustomResourceDefinition & Anvil
			metrics.ResourceCreated("CustomResourceDefinition"),
			metrics.ResourceCreated("Anvil"),
			metrics.ResourceCreated("Namespace"))
		return err
	})
	if err != nil {
		nt.T.Error(err)
	}

	// Update the CRD from version v1 to version v2.
	crdFile = filepath.Join(".", "..", "testdata", "customresources", dir, "anvil-crd-v2.yaml")
	crdContent, err = ioutil.ReadFile(crdFile)
	if err != nil {
		nt.T.Fatal(err)
	}
	nt.RootRepos[configsync.RootSyncName].AddFile("acme/cluster/anvil-crd.yaml", crdContent)
	nt.RootRepos[configsync.RootSyncName].CommitAndPush("Updating the Anvil CRD")
	nt.WaitForRepoSyncs()

	err = nt.Validate("e2e-test-anvil", "prod", anvilCR("v2", "", 10))
	if err != nil {
		nt.T.Fatal(err)
	}
	err = nt.Validate("anvils.acme.com", "", fake.CustomResourceDefinitionV1Object(), hasTwoVersions)
	if err != nil {
		nt.T.Fatal(err)
	}

	// Update CRD and CR to only support V2
	crdFile = filepath.Join(".", "..", "testdata", "customresources", dir, "anvil-crd-only-v2.yaml")
	crdContent, err = ioutil.ReadFile(crdFile)
	if err != nil {
		nt.T.Fatal(err)
	}
	nt.RootRepos[configsync.RootSyncName].AddFile("acme/cluster/anvil-crd.yaml", crdContent)
	nt.RootRepos[configsync.RootSyncName].Add("acme/namespaces/prod/anvil.yaml", anvilCR("v2", "e2e-test-anvil", 10))
	nt.RootRepos[configsync.RootSyncName].CommitAndPush("Update the Anvil CRD and CR")
	nt.WaitForRepoSyncs()

	err = nt.Validate("anvils.acme.com", "", fake.CustomResourceDefinitionV1Object(), nomostest.IsEstablished, hasTwoVersions)
	if err != nil {
		nt.T.Fatal(err)
	}

	err = nt.Validate("e2e-test-anvil", "prod", anvilCR("v2", "", 10))
	if err != nil {
		nt.T.Fatal(err)
	}

	// Remove CRD and CR
	nt.RootRepos[configsync.RootSyncName].Remove("acme/cluster/anvil-crd.yaml")
	nt.RootRepos[configsync.RootSyncName].Remove("acme/namespaces/prod/anvil.yaml")
	nt.RootRepos[configsync.RootSyncName].CommitAndPush("Remove the Anvil CRD and CR")
	nt.WaitForRepoSyncs()

	// Validate the CustomResource is also deleted from cluster.
	err = nt.ValidateNotFound("anvils.acme.com", "", fake.CustomResourceDefinitionV1Object())
	if err != nil {
		nt.T.Fatal(err)
	}
}
func TestAddUpdateNamespaceScopedCRDV1(t *testing.T) {
	nt := nomostest.New(t, nomostesting.Reconciliation1)
	addUpdateNamespaceScopedCRD(nt, "v1_crds", "anvil-crd.yaml")
}

func TestAddUpdateNamespaceScopedCRDV1Beta1(t *testing.T) {
	nt := nomostest.New(t, nomostesting.Reconciliation1)
	support, err := nt.SupportV1Beta1CRDAndRBAC()
	if err != nil {
		nt.T.Fatal("failed to check the supported CRD versions")
	}
	if support {
		addUpdateNamespaceScopedCRD(nt, "v1beta1_crds", "anvil-crd-v1.yaml")
	}
}

func TestLargeCRD(t *testing.T) {
	nt := nomostest.New(t, nomostesting.Reconciliation1)

	for _, file := range []string{"challenges-acme-cert-manager-io.yaml", "solrclouds-solr-apache-org.yaml"} {
		crdFile := filepath.Join(".", "..", "testdata", "customresources", file)
		crdContent, err := ioutil.ReadFile(crdFile)
		if err != nil {
			nt.T.Fatal(err)
		}
		nt.RootRepos[configsync.RootSyncName].AddFile(fmt.Sprintf("acme/cluster/%s", file), crdContent)
	}
	nt.RootRepos[configsync.RootSyncName].CommitAndPush("Adding two large CRDs")
	nt.WaitForRepoSyncs()
	nt.RenewClient()

	err := nomostest.WatchObject(nt, kinds.CustomResourceDefinitionV1(), "challenges.acme.cert-manager.io", "", nil,
		nomostest.WatchTimeout(30*time.Second))
	if err != nil {
		nt.T.Fatal(err)
	}

	err = nomostest.WatchObject(nt, kinds.CustomResourceDefinitionV1(), "solrclouds.solr.apache.org", "", nil,
		nomostest.WatchTimeout(30*time.Second))
	if err != nil {
		nt.T.Fatal(err)
	}

	// Validate multi-repo metrics.
	err = nt.ValidateMetrics(nomostest.SyncMetricsToLatestCommit(nt), func() error {
		err = nt.ValidateMultiRepoMetrics(nomostest.DefaultRootReconcilerName,
			nt.DefaultRootSyncObjectCount()+2, // 2 for the test CustomResourceDefinitions
			metrics.ResourceCreated("CustomResourceDefinition"),
			metrics.ResourceCreated("CustomResourceDefinition"))
		if err != nil {
			return err
		}
		return nt.ValidateErrorMetricsNotFound()
	})
	if err != nil {
		nt.T.Error(err)
	}

	// update one CRD
	crdFile := filepath.Join(".", "..", "testdata", "customresources", "challenges-acme-cert-manager-io_with_new_label.yaml")
	crdContent, err := ioutil.ReadFile(crdFile)
	if err != nil {
		nt.T.Fatal(err)
	}
	nt.RootRepos[configsync.RootSyncName].AddFile("acme/cluster/challenges-acme-cert-manager-io.yaml", crdContent)
	nt.RootRepos[configsync.RootSyncName].CommitAndPush("Update label for one CRD")
	nt.WaitForRepoSyncs()

	err = nomostest.WatchObject(nt, kinds.CustomResourceDefinitionV1(), "challenges.acme.cert-manager.io", "",
		[]nomostest.Predicate{nomostest.HasLabel("random-key", "random-value")},
		nomostest.WatchTimeout(30*time.Second))
	if err != nil {
		nt.T.Fatal(err)
	}
}

func hasRule(name string) nomostest.Predicate {
	return func(o client.Object) error {
		if o == nil {
			return nomostest.ErrObjectNotFound
		}
		vwc, ok := o.(*admissionv1.ValidatingWebhookConfiguration)
		if !ok {
			return nomostest.WrongTypeErr(o, &admissionv1.ValidatingWebhookConfiguration{})
		}
		for _, w := range vwc.Webhooks {
			if w.Name == name {
				return nil
			}
		}
		return errors.Errorf("missing ValidatingWebhook %q", name)
	}
}

func hasTwoVersions(obj client.Object) error {
	if obj == nil {
		return nomostest.ErrObjectNotFound
	}
	crd := obj.(*apiextensionsv1.CustomResourceDefinition)
	if len(crd.Spec.Versions) != 2 {
		return errors.New("the CRD should contain 2 versions")
	}
	if crd.Spec.Versions[0].Name != "v1" || crd.Spec.Versions[1].Name != "v2" {
		return errors.New("incorrect versions for CRD")
	}
	return nil
}

func clusteranvilCR(version, name string, weight int64) *unstructured.Unstructured {
	u := anvilCR(version, name, weight)
	gvk := u.GroupVersionKind()
	gvk.Kind = "ClusterAnvil"
	u.SetGroupVersionKind(gvk)
	return u
}
