# Copyright 2022 Google LLC
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#      http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# Dockerfile for the prow test environment

# We'd use the kubekins-e2e straight away, but it's missing the 'kind' binary.

# Ensure versions in this file match those in Makefile.prow. This is not
# neccessarily a strict requirement since the versions passed in the Makefile
# will override any defaults set here, however having the same versions set
# between these two files will eliminate any confusion on what versions are
# actually being used.

ARG KUBEKINS_REGISTRY="gke-test-infra"
ARG KUBEKINS="v20220204-29ea0e1be-1.22"

FROM gcr.io/${KUBEKINS_REGISTRY}/kubekins-e2e:${KUBEKINS}

ARG KIND_RELEASE="v0.14.0"
RUN wget -q -O /bin/kind https://kind.sigs.k8s.io/dl/${KIND_RELEASE}/kind-linux-amd64
RUN chmod +x /bin/kind
