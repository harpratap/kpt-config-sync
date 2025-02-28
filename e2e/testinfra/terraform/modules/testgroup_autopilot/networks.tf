/**
 * Copyright 2022 Google LLC
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *      http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

resource "google_compute_network" "e2e-network" {
  name                    = "${var.prefix}-e2e-network-${local.suffix}"
  auto_create_subnetworks = false
}

resource "google_compute_subnetwork" "e2e-subnetwork-default" {
  name          = "${var.prefix}-e2e-subnetwork-${local.suffix}"
  ip_cidr_range = "10.0.0.0/20"
  region        = "us-central1"
  network       = google_compute_network.e2e-network.id
  description = "Subnetwork for use in e2e test clusters"
  private_ip_google_access = false
}
