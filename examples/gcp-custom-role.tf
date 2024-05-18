resource "google_project_iam_custom_role" "k8s-pvc-tagger" {
  project     = var.gcp_project
  role_id     = "k8s-pvc-tagger"
  title       = "k8s-pvc-tagger"
  description = "A Custom role with minimum permission set for k8s-pvc-tagger"
  permissions = [
    "compute.disks.get",
    "compute.disks.list",
    "compute.disks.setLabels",
  ]
}
