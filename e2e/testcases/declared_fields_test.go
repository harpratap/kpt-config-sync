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
	"testing"

	corev1 "k8s.io/api/core/v1"
	nomostesting "kpt.dev/configsync/e2e/nomostest/testing"
	"kpt.dev/configsync/pkg/api/configsync"
	"kpt.dev/configsync/pkg/kinds"

	"kpt.dev/configsync/e2e/nomostest"
	"kpt.dev/configsync/e2e/nomostest/ntopts"
	"kpt.dev/configsync/pkg/testing/fake"
)

func TestDeclaredFieldsPod(t *testing.T) {
	nt := nomostest.New(t, nomostesting.Reconciliation1, ntopts.Unstructured)

	namespace := fake.NamespaceObject("bookstore")
	nt.RootRepos[configsync.RootSyncName].Add("acme/ns.yaml", namespace)
	// We use literal YAML here instead of an object as:
	// 1) If we used a literal struct the protocol field would implicitly be added.
	// 2) It's really annoying to specify this as Unstructureds.
	nt.RootRepos[configsync.RootSyncName].AddFile("acme/pod.yaml", []byte(`
apiVersion: v1
kind: Pod
metadata:
  name: nginx
  namespace: bookstore
spec:
  containers:
  - image: nginx:1.7.9
    name: nginx
    ports:
    - containerPort: 80
`))
	nt.RootRepos[configsync.RootSyncName].CommitAndPush("add pod missing protocol from port")
	nt.WaitForRepoSyncs()

	// Parse the pod yaml into an object
	pod := nt.RootRepos[configsync.RootSyncName].Get("acme/pod.yaml")

	err := nt.Validate(pod.GetName(), pod.GetNamespace(), &corev1.Pod{})
	if err != nil {
		nt.T.Fatal(err)
	}

	nt.RootRepos[configsync.RootSyncName].Remove("acme/pod.yaml")
	nt.RootRepos[configsync.RootSyncName].CommitAndPush("Remove the pod")
	nt.WaitForRepoSyncs()

	err = nomostest.WatchForNotFound(nt, kinds.Pod(), pod.GetName(), pod.GetNamespace())
	if err != nil {
		nt.T.Fatal(err)
	}
}
