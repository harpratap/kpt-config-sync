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

# Build all Config Sync go binaries
FROM golang:1.17 as bins

WORKDIR /workspace

COPY . .

# Version string to embed in built binary.
ARG VERSION
ARG HELM_INFLATOR_FUNCTIOPN_VERSION=v0.2.0

ARG HELM_VERSION=v3.9.0
ARG KUSTOMIZE_VERSION=v4.5.2

# Install Helm
RUN wget https://get.helm.sh/helm-${HELM_VERSION}-linux-amd64.tar.gz -O /tmp/helm-${HELM_VERSION}-linux-amd64.tar.gz && \
  tar -zxvf /tmp/helm-${HELM_VERSION}-linux-amd64.tar.gz -C /tmp && \
  mv /tmp/linux-amd64/helm /usr/local/bin/helm && \
  rm -rf /tmp/linux-amd64 /tmp/helm-${HELM_VERSION}-linux-amd64.tar.gz

# Install Kustomize
RUN wget https://github.com/kubernetes-sigs/kustomize/releases/download/kustomize%2F${KUSTOMIZE_VERSION}/kustomize_${KUSTOMIZE_VERSION}_linux_amd64.tar.gz -O /tmp/kustomize_${KUSTOMIZE_VERSION}_linux_amd64.tar.gz && \
  tar -zxvf /tmp/kustomize_${KUSTOMIZE_VERSION}_linux_amd64.tar.gz -C /tmp && \
  mv /tmp/kustomize /usr/local/bin/kustomize && \
  rm /tmp/kustomize_${KUSTOMIZE_VERSION}_linux_amd64.tar.gz

# Install the render-helm-chart function.
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 GO111MODULE=on \
  go install github.com/GoogleContainerTools/kpt-functions-catalog/functions/go/render-helm-chart@${HELM_INFLATOR_FUNCTIOPN_VERSION}

# Build all our stuff.
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 GO111MODULE=on \
  go install \
    -mod=vendor \
    -ldflags "-X kpt.dev/configsync/pkg/version.VERSION=${VERSION}" \
    ./cmd/nomos \
    ./cmd/reconciler \
    ./cmd/reconciler-manager \
    ./cmd/hydration-controller \
    ./cmd/admission-webhook \
    ./cmd/oci-sync \
    ./cmd/helm-sync

# Hydration controller image
FROM gcr.io/distroless/static:nonroot as hydration-controller
WORKDIR /
COPY --from=bins /go/bin/hydration-controller .
COPY --from=bins /go/bin/render-helm-chart /usr/local/bin/render-helm-chart
COPY --from=bins /usr/local/bin/helm /usr/local/bin/helm
COPY --from=bins /usr/local/bin/kustomize /usr/local/bin/kustomize

# License file required for on-prem release.
COPY LICENSE LICENSE
COPY LICENSES.txt LICENSES.txt

# Switch to non-root user
USER 1000

ENTRYPOINT ["/hydration-controller"]

# OCI-sync image
FROM gcr.io/distroless/static:latest as oci-sync
# Setting HOME ensures that whatever UID this ultimately runs as can write files.
ENV HOME=/tmp
WORKDIR /
COPY --from=bins /go/bin/oci-sync .

# License file required for on-prem release.
COPY LICENSE LICENSE
COPY LICENSES.txt LICENSES.txt

# Switch to non-root user
USER 1000

ENTRYPOINT ["/oci-sync"]

# Helm-sync image
FROM gcr.io/distroless/static:latest as helm-sync
# Setting HOME ensures that whatever UID this ultimately runs as can write files.
ENV HOME=/tmp
WORKDIR /
COPY --from=bins /go/bin/helm-sync .
COPY --from=bins /usr/local/bin/helm /usr/local/bin/helm

# License file required for on-prem release.
COPY LICENSE LICENSE
COPY LICENSES.txt LICENSES.txt

# Switch to non-root user
USER 1000

ENTRYPOINT ["/helm-sync"]

# Hydration controller image with shell
# TODO: this is a temporary image. Replace with the new debian-base image when it's available
FROM gcr.io/config-management-release/debian-base:bullseye-v1.4.2-gke.7-upgrade as hydration-controller-with-shell
WORKDIR /
COPY --from=bins /go/bin/hydration-controller .
COPY --from=bins /go/bin/render-helm-chart /usr/local/bin/render-helm-chart
COPY --from=bins /usr/local/bin/helm /usr/local/bin/helm
COPY --from=bins /usr/local/bin/kustomize /usr/local/bin/kustomize
RUN apt-get update && apt-get install -y git

# License file required for on-prem release.
COPY LICENSE LICENSE
COPY LICENSES.txt LICENSES.txt

# Switch to non-root user
USER 1000

ENTRYPOINT ["/hydration-controller"]

# Reconciler image
FROM gcr.io/distroless/static:nonroot as reconciler
WORKDIR /
COPY --from=bins /go/bin/reconciler .

# License file required for on-prem release.
COPY LICENSE LICENSE
COPY LICENSES.txt LICENSES.txt

# Switch to non-root user
USER 1000

ENTRYPOINT ["/reconciler"]

# Reconciler Manager image
FROM gcr.io/distroless/static:nonroot as reconciler-manager
WORKDIR /
COPY --from=bins /go/bin/reconciler-manager reconciler-manager
USER nonroot:nonroot

# License file required for on-prem release.
COPY LICENSE LICENSE
COPY LICENSES.txt LICENSES.txt

ENTRYPOINT ["/reconciler-manager"]

# Admission Webhook image
FROM gcr.io/distroless/static:nonroot as admission-webhook
WORKDIR /
COPY --from=bins /go/bin/admission-webhook admission-webhook
USER nonroot:nonroot

# License file required for on-prem release.
COPY LICENSE LICENSE
COPY LICENSES.txt LICENSES.txt

ENTRYPOINT ["/admission-webhook"]

# Nomos image
# Not used by Config Sync backend components. Intended for use cases with the
# nomos CLI (e.g. containerized CI/CD)
# TODO: this is a temporary image. Replace with the new debian-base image when it's available
FROM gcr.io/config-management-release/debian-base:bullseye-v1.4.2-gke.7-upgrade as nomos

# https://github.com/GoogleCloudPlatform/google-cloud-go/issues/791#issuecomment-353689746
RUN apt-get update && \
  apt-get install -y bash git

RUN mkdir -p /opt/nomos/bin
ENV PATH="/opt/nomos/bin:${PATH}"
WORKDIR /opt/nomos/bin

# Nomos binary
COPY --from=bins /go/bin/nomos nomos

# License file required for on-prem release.
COPY LICENSE LICENSE
COPY LICENSES.txt LICENSES.txt

# Set up a HOME directory for non-root user
RUN mkdir -p /nomos
RUN chown 1000 /nomos
ENV HOME="/nomos"

# Switch to non-root user
USER 1000

ENTRYPOINT ["/opt/nomos/bin/nomos"]
