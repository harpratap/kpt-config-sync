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
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"kpt.dev/configsync/e2e/nomostest"
	"kpt.dev/configsync/e2e/nomostest/ntopts"
	"kpt.dev/configsync/e2e/nomostest/policy"
	"kpt.dev/configsync/e2e/nomostest/taskgroup"
	nomostesting "kpt.dev/configsync/e2e/nomostest/testing"
	"kpt.dev/configsync/pkg/api/configmanagement"
	"kpt.dev/configsync/pkg/api/configsync"
	"kpt.dev/configsync/pkg/api/configsync/v1beta1"
	"kpt.dev/configsync/pkg/core"
	"kpt.dev/configsync/pkg/declared"
	"kpt.dev/configsync/pkg/kinds"
	"kpt.dev/configsync/pkg/reconcilermanager"
	"kpt.dev/configsync/pkg/testing/fake"
	"sigs.k8s.io/cli-utils/pkg/kstatus/status"
	"sigs.k8s.io/cli-utils/pkg/object/mutation"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// TestComposition validates multi-layer composition of R*Syncs sharing a single
// git repo, using different directories.
//
// ├── repos
// │   └── config-management-system
// │       └── root-sync
// │           ├── acme
// │           │   └── level-0
// │           │       ├── rootsync-level-1.yaml
// │           │       ├── ns-test-ns.yaml
// │           │       ├── rb-test-ns-level-2.yaml
// │           │       ├── rb-test-ns-level-3.yaml
// │           │       └── .gitkeep
// │           ├── level-1
// │           │   ├── reposync-level-2.yaml
// │           │   └── .gitkeep
// │           ├── level-2
// │           │   ├── reposync-level-3.yaml
// │           │   └── .gitkeep
// │           └── level-3
// │               ├── configmap-level-4.yaml
// │               └── .gitkeep
//
// This tests multiple things:
// 1. RootSync -> RootSyncs -> RepoSyncs -> RepoSyncs
// 2. RepoSyncs & RepoSyncs can share a repository, using different directories.
// 3. RepoSyncs can share an ssh-key secret
// 4. R*Sync status isn't updated after sync without external input.
func TestComposition(t *testing.T) {
	nt := nomostest.New(t,
		nomostesting.MultiRepos,
		ntopts.Unstructured,
		ntopts.WithDelegatedControl,
		ntopts.RepoSyncPermissions(policy.RepoSyncAdmin(), policy.CoreAdmin()), // NS reconciler manages RepoSyncs and ConfigMaps
		ntopts.RootRepo(configsync.RootSyncName))

	lvl0NN := nomostest.RootSyncNN(configsync.RootSyncName)
	lvl1NN := nomostest.RootSyncNN("level-1")
	lvl2NN := types.NamespacedName{Namespace: testNs, Name: "level-2"}
	lvl3NN := types.NamespacedName{Namespace: testNs, Name: "level-3"}
	lvl4NN := types.NamespacedName{Namespace: testNs, Name: "level-4"}

	lvl0Repo := nt.RootRepos[lvl0NN.Name]

	lvl0Sync := nomostest.RootSyncObjectV1Beta1FromRootRepo(nt, lvl0NN.Name)
	lvl0Sync.Spec.Git.Dir = nomostest.AcmeDir
	// Use a subdirectory under the root-sync repo to make cleanup easy
	lvl0SubDir := filepath.Join(lvl0Sync.Spec.Git.Dir, "level-0")

	lvl1Sync := nomostest.RootSyncObjectV1Beta1FromOtherRootRepo(nt, lvl1NN.Name, lvl0NN.Name)
	lvl1Sync.Spec.Git.Dir = lvl1NN.Name

	lvl2RB := nomostest.RepoSyncRoleBinding(lvl2NN)
	lvl2Sync := nomostest.RepoSyncObjectV1Beta1FromOtherRootRepo(nt, lvl2NN, lvl0NN.Name)
	lvl2Sync.Spec.Git.Dir = lvl2NN.Name

	lvl3RB := nomostest.RepoSyncRoleBinding(lvl3NN)
	lvl3Sync := nomostest.RepoSyncObjectV1Beta1FromOtherRootRepo(nt, lvl3NN, lvl0NN.Name)
	lvl3Sync.Spec.Git.Dir = lvl3NN.Name

	// RepoSync level-2 depends on RoleBinding level-2.
	// RepoSync level-3 depends on RoleBinding level-3.
	// But the RoleBindings are managed by RootSync root-sync (level-0),
	// and Config Sync doesn't support external dependencies.
	// Since RepoSync level-3 is managed by RepoSync level-2,
	// and RepoSync level-2 is manage by RootSync level-1,
	// and RootSync level-1 is manage by RootSync root-sync (level-0),
	// we need to make RepoSync level-1 depend on both RoleBindings,
	// so the RoleBindings are deleted after both RepoSyncs.
	if err := nomostest.SetDependencies(lvl1Sync, lvl2RB, lvl3RB); err != nil {
		nt.T.Fatal(err)
	}

	// All R*Syncs in this test will use the same repository, with different directories.
	// This function returns the latest sha1 of the shared root repo.
	rootSha1Fn := func(nt *nomostest.NT, _ types.NamespacedName) (string, error) {
		return lvl0Repo.Hash(), nil
	}

	// Remove files to prune objects before the R*Syncs are deleted.
	// Use seperate functions to allow independent failure to be tollerated,
	// which can happen if the previous test failed when partially complete.
	// Cleanups execute in the reverse order they are added.
	t.Cleanup(func() {
		if lvl0Repo.Exists(lvl0SubDir) {
			nt.T.Logf("Cleaning up %s %s...", lvl0Sync.Kind, lvl0NN)
			removeDirContents(lvl0Repo, lvl0SubDir)
			nt.T.Logf("Waiting for %s %s to be synced...", lvl0Sync.Kind, lvl0NN)
			waitForSync(nt, rootSha1Fn, lvl0Sync)
		}
	})
	t.Cleanup(func() {
		if lvl0Repo.Exists(lvl1Sync.Spec.Git.Dir) {
			nt.T.Logf("Cleaning up %s %s...", lvl1Sync.Kind, lvl1NN)
			removeDirContents(lvl0Repo, lvl1Sync.Spec.Git.Dir)
			nt.T.Logf("Waiting for %s %s to be synced...", lvl1Sync.Kind, lvl1NN)
			waitForSync(nt, rootSha1Fn, lvl1Sync)
		}
	})
	t.Cleanup(func() {
		if lvl0Repo.Exists(lvl2Sync.Spec.Git.Dir) {
			nt.T.Logf("Cleaning up %s %s...", lvl2Sync.Kind, lvl2NN)
			removeDirContents(lvl0Repo, lvl2Sync.Spec.Git.Dir)
			nt.T.Logf("Waiting for %s %s to be synced...", lvl2Sync.Kind, lvl2NN)
			waitForSync(nt, rootSha1Fn, lvl2Sync)
		}
	})
	t.Cleanup(func() {
		if lvl0Repo.Exists(lvl3Sync.Spec.Git.Dir) {
			nt.T.Logf("Cleaning up %s %s...", lvl3Sync.Kind, lvl3NN)
			removeDirContents(lvl0Repo, lvl3Sync.Spec.Git.Dir)
			nt.T.Logf("Waiting for %s %s to be synced...", lvl3Sync.Kind, lvl3NN)
			waitForSync(nt, rootSha1Fn, lvl3Sync)
		}
	})
	// Print reconciler logs for R*Syncs that aren't in nt.RootRepos or nt.NonRootRepos.
	t.Cleanup(func() {
		if t.Failed() {
			nt.PodLogs(configmanagement.ControllerNamespace, core.RootReconcilerName(lvl1NN.Name),
				reconcilermanager.Reconciler, false)
			nt.PodLogs(configmanagement.ControllerNamespace, core.NsReconcilerName(lvl2NN.Namespace, lvl2NN.Name),
				reconcilermanager.Reconciler, false)
			nt.PodLogs(configmanagement.ControllerNamespace, core.NsReconcilerName(lvl3NN.Namespace, lvl3NN.Name),
				reconcilermanager.Reconciler, false)
		}
	})

	nt.T.Logf("Adding Namespace & RoleBindings for RepoSyncs")
	lvl0Repo.Add(filepath.Join(lvl0SubDir, fmt.Sprintf("ns-%s.yaml", testNs)), fake.NamespaceObject(testNs))
	lvl0Repo.Add(filepath.Join(lvl0SubDir, fmt.Sprintf("rb-%s-%s.yaml", testNs, lvl2NN.Name)), lvl2RB)
	lvl0Repo.Add(filepath.Join(lvl0SubDir, fmt.Sprintf("rb-%s-%s.yaml", testNs, lvl3NN.Name)), lvl3RB)
	lvl0Repo.CommitAndPush("Adding Namespace & RoleBindings for RepoSyncs")

	nt.T.Log("Waiting for R*Syncs to be synced...")
	waitForSync(nt, rootSha1Fn, lvl0Sync)

	nt.T.Log("Validating synced objects are reconciled...")
	validateStatusCurrent(nt, lvl0Repo.GetAll(lvl0SubDir, true)...)

	nt.T.Logf("Creating Secret for RepoSync: %s", lvl2NN)
	nomostest.CreateNamespaceSecret(nt, lvl2NN.Namespace)

	// lvl1 RootSync
	lvl1Path := filepath.Join(lvl0SubDir, fmt.Sprintf("rootsync-%s.yaml", lvl1NN.Name))
	nt.T.Logf("Adding RootSync %s to the shared repository: %s", lvl1NN.Name, lvl1Path)
	lvl0Repo.Add(lvl1Path, lvl1Sync)
	lvl0Repo.AddEmptyDir(lvl1Sync.Spec.Git.Dir)
	lvl0Repo.CommitAndPush(fmt.Sprintf("Adding RootSync: %s", lvl1NN))

	nt.T.Log("Waiting for R*Syncs to be synced...")
	waitForSync(nt, rootSha1Fn, lvl0Sync, lvl1Sync)

	nt.T.Log("Validating synced objects are reconciled...")
	validateStatusCurrent(nt, lvl0Repo.GetAll(lvl0SubDir, true)...)
	// lvl1Sync.Spec.Git.Dir contains no yaml yet, so we don't need to test it for reconciliation yet.

	// lvl2 RepoSync
	lvl2Path := filepath.Join(lvl1Sync.Spec.Git.Dir, fmt.Sprintf("reposync-%s.yaml", lvl2NN.Name))
	nt.T.Logf("Adding RepoSync %s to the shared repository: %s", lvl2NN.Name, lvl2Path)
	lvl0Repo.Add(lvl2Path, lvl2Sync)
	lvl0Repo.AddEmptyDir(lvl2Sync.Spec.Git.Dir)
	lvl0Repo.CommitAndPush(fmt.Sprintf("Adding RepoSync: %s", lvl2NN))

	nt.T.Log("Waiting for R*Syncs to be synced...")
	waitForSync(nt, rootSha1Fn, lvl0Sync, lvl1Sync, lvl2Sync)

	nt.T.Log("Validating synced objects are reconciled...")
	validateStatusCurrent(nt, lvl0Repo.GetAll(lvl0SubDir, true)...)
	validateStatusCurrent(nt, lvl0Repo.GetAll(lvl1Sync.Spec.Git.Dir, true)...)
	// lvl2Sync.Spec.Git.Dir contains no yaml yet, so we don't need to test it for reconciliation yet.

	// lvl3 RepoSync
	lvl3Path := filepath.Join(lvl2Sync.Spec.Git.Dir, fmt.Sprintf("reposync-%s.yaml", lvl3NN.Name))
	nt.T.Logf("Adding RepoSync %s to the shared repository: %s", lvl3NN.Name, lvl3Path)
	lvl0Repo.Add(lvl3Path, lvl3Sync)
	lvl0Repo.AddEmptyDir(lvl3Sync.Spec.Git.Dir)
	lvl0Repo.CommitAndPush(fmt.Sprintf("Adding RepoSync: %s", lvl3NN))

	nt.T.Log("Waiting for R*Syncs to be synced...")
	waitForSync(nt, rootSha1Fn, lvl0Sync, lvl1Sync, lvl2Sync, lvl3Sync)

	nt.T.Log("Validating synced objects are reconciled...")
	validateStatusCurrent(nt, lvl0Repo.GetAll(lvl0SubDir, true)...)
	validateStatusCurrent(nt, lvl0Repo.GetAll(lvl1Sync.Spec.Git.Dir, true)...)
	validateStatusCurrent(nt, lvl0Repo.GetAll(lvl2Sync.Spec.Git.Dir, true)...)
	// lvl2Sync.Spec.Git.Dir contains no yaml yet, so we don't need to test it for reconciliation yet.

	// lvl4 ConfigMap
	lvl4ConfigMap := &corev1.ConfigMap{}
	lvl4ConfigMap.SetGroupVersionKind(corev1.SchemeGroupVersion.WithKind("ConfigMap"))
	lvl4ConfigMap.SetNamespace(testNs)
	lvl4ConfigMap.SetName(lvl4NN.Name)
	lvl4ConfigMap.Data = map[string]string{"key": "value"}
	lvl4Path := filepath.Join(lvl3Sync.Spec.Git.Dir, fmt.Sprintf("configmap-%s.yaml", lvl4NN.Name))
	nt.T.Logf("Adding ConfigMap %s to the shared repository: %s", lvl4NN.Name, lvl4Path)
	lvl0Repo.Add(lvl4Path, lvl4ConfigMap)
	lvl0Repo.CommitAndPush(fmt.Sprintf("Adding ConfigMap: %s", lvl4NN.Name))

	nt.T.Log("Waiting for R*Syncs to be synced...")
	waitForSync(nt, rootSha1Fn, lvl0Sync, lvl1Sync, lvl2Sync, lvl3Sync)

	nt.T.Log("Validating synced objects are reconciled...")
	validateStatusCurrent(nt, lvl0Repo.GetAll(lvl0SubDir, true)...)
	validateStatusCurrent(nt, lvl0Repo.GetAll(lvl1Sync.Spec.Git.Dir, true)...)
	validateStatusCurrent(nt, lvl0Repo.GetAll(lvl2Sync.Spec.Git.Dir, true)...)
	validateStatusCurrent(nt, lvl0Repo.GetAll(lvl3Sync.Spec.Git.Dir, true)...)

	// Validate that the R*Syncs and ConfigMap exist, are reconciled, and have the right manager.
	managedObjs := map[gvknn]manager{
		{kinds.RootSyncV1Beta1(), lvl0NN}: {}, // no manager
		{kinds.RootSyncV1Beta1(), lvl1NN}: {declared.RootReconciler, lvl0NN.Name},
		{kinds.RepoSyncV1Beta1(), lvl2NN}: {declared.RootReconciler, lvl1NN.Name},
		{kinds.RepoSyncV1Beta1(), lvl3NN}: {declared.Scope(lvl2NN.Namespace), lvl2NN.Name},
		{kinds.ConfigMap(), lvl4NN}:       {declared.Scope(lvl3NN.Namespace), lvl3NN.Name},
	}

	synedObjs := make(map[gvknn]*unstructured.Unstructured, len(managedObjs))

	for id, mgr := range managedObjs {
		var predicates []nomostest.Predicate
		predicates = append(predicates, nomostest.StatusEquals(nt, status.CurrentStatus))
		if mgr == (manager{}) {
			nt.T.Logf("Ensure %q exists, is reconciled, and is not managed", id)
			predicates = append(predicates, nomostest.IsNotManaged(nt))
		} else {
			nt.T.Logf("Ensure %q exists, is reconciled, and is managed by %q", id, mgr)
			predicates = append(predicates, nomostest.IsManagedBy(nt, mgr.Scope, mgr.Name))
		}
		obj := &unstructured.Unstructured{}
		obj.SetGroupVersionKind(id.GroupVersionKind)
		err := nt.Validate(id.Name, id.Namespace, obj, predicates...)
		if err != nil {
			nt.T.Fatal(err)
		}
		synedObjs[id] = obj
	}

	nt.T.Log("Waiting 1m to make sure there's no unnecessary updates...")
	time.Sleep(1 * time.Minute)

	for id, synedObj := range synedObjs {
		nt.T.Logf("Ensure %q exists, is current, and its ResourceVersion has not changed", id)
		obj := &unstructured.Unstructured{}
		obj.SetGroupVersionKind(id.GroupVersionKind)
		err := nt.Validate(id.Name, id.Namespace, obj,
			nomostest.StatusEquals(nt, status.CurrentStatus),
			nomostest.ResourceVersionEquals(nt, synedObj.GetResourceVersion()))
		if err != nil {
			// Error, not Fatal, so we can see all the diffs when it fails.
			nt.T.Error(err)
			// Log the diff so we can see what fields changed.
			nt.T.Logf("Diff (- Expected, + Actual):\n%s", cmp.Diff(synedObj, obj))
		}
	}
}

type manager struct {
	Scope declared.Scope
	Name  string
}

type gvknn struct {
	schema.GroupVersionKind
	types.NamespacedName
}

// ToResourceReference converts from gvknn to ResourceReference.
func (id gvknn) ToResourceReference() mutation.ResourceReference {
	apiVersion, kind := id.ToAPIVersionAndKind()
	return mutation.ResourceReference{
		APIVersion: apiVersion,
		Kind:       kind,
		Name:       id.Name,
		Namespace:  id.Namespace,
	}
}

// String returns the gvknn in ResourceReference string format.
func (id gvknn) String() string {
	return id.ToResourceReference().String()
}

// waitForSync waits for the specified R*Syncs to be Synced.
//
// The reason we can't just use nt.WaitForRepoSyncs is that the R*Syncs for this
// test are not all in nt.RootRepos or nt.NonRootRepos, because they're all
// sharing the same repository.
//
// So this function uses the same sha1Func for all R*Syncs.
func waitForSync(nt *nomostest.NT, sha1Func nomostest.Sha1Func, objs ...client.Object) {
	nt.T.Helper()
	var wg sync.WaitGroup
	for _, obj := range objs {
		switch rsync := obj.(type) {
		case *v1beta1.RootSync:
			wg.Add(1)
			go func() {
				defer wg.Done()
				nt.WaitForSync(kinds.RootSyncV1Beta1(), rsync.Name, rsync.Namespace,
					nt.DefaultWaitTimeout, sha1Func, nomostest.RootSyncHasStatusSyncCommit,
					&nomostest.SyncDirPredicatePair{
						Dir:       rsync.Spec.Git.Dir,
						Predicate: nomostest.RootSyncHasStatusSyncDirectory,
					})
			}()
		case *v1beta1.RepoSync:
			wg.Add(1)
			go func() {
				defer wg.Done()
				nt.WaitForSync(kinds.RepoSyncV1Beta1(), rsync.Name, rsync.Namespace,
					nt.DefaultWaitTimeout, sha1Func, nomostest.RepoSyncHasStatusSyncCommit,
					&nomostest.SyncDirPredicatePair{
						Dir:       rsync.Spec.Git.Dir,
						Predicate: nomostest.RepoSyncHasStatusSyncDirectory,
					})
			}()
		default:
			nt.T.Fatal("Invalid R*Sync type: %T", obj)
		}
	}
	wg.Wait()
	if nt.T.Failed() {
		nt.T.Fatal("R*Syncs not synced")
	}
}

func validateStatusCurrent(nt *nomostest.NT, objs ...client.Object) {
	tg := taskgroup.New()
	for _, obj := range objs {
		nn := client.ObjectKeyFromObject(obj)
		gvk, err := kinds.Lookup(obj, nt.Client.Scheme())
		require.NoError(nt.T, err)
		tg.Go(func() error {
			return nomostest.WatchForCurrentStatus(nt, gvk, nn.Name, nn.Namespace)
		})
	}
	err := tg.Wait()
	if err != nil {
		nt.T.Fatal(err)
	}
}

// removeDirContents deletes all the files in a directory (recursively), but
// keeps the directory.
func removeDirContents(repo *nomostest.Repository, dirPath string) {
	repo.Remove(dirPath)
	repo.AddEmptyDir(dirPath)
	repo.CommitAndPush(fmt.Sprintf("Remove dir contents: %s ", dirPath))
}
