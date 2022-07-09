# k8s-pvc-tagger

NOTE: This project was originally named `k8s-aws-ebs-tagger` but was renamed to `k8s-pvc-tagger` as the scope has expanded to more than aws ebs volumes.

A utility to tag PVC volumes based on the PVC's `k8s-pvc-tagger/tags` annotation

![Go](https://github.com/mtougeron/k8s-pvc-tagger/workflows/Go/badge.svg) ![Gosec](https://github.com/mtougeron/k8s-pvc-tagger/workflows/Gosec/badge.svg) ![ContainerScan](https://github.com/mtougeron/k8s-pvc-tagger/workflows/ContainerScan/badge.svg) [![GitHub tag](https://img.shields.io/github/v/tag/mtougeron/k8s-pvc-tagger)](https://github.com/mtougeron/k8s-pvc-tagger/tags/)

The `k8s-pvc-tagger` watches for new PersistentVolumeClaims and when new AWS EBS volumes are created it adds tags based on the PVC's `k8s-pvc-tagger/tags` annotation to the created EBS volume. Other cloud provider and volume times are coming soon.

### How to set tags

#### cmdline args

`--default-tags` - A json or csv encoded key/value map of the tags to set by default on EBS Volumes. Values can be overwritten by the `k8s-pvc-tagger/tags` annotation.

`--tag-format` - Either `json` or `csv` for the format the `k8s-pvc-tagger/tags` and `--default-tags` are in.

`--allow-all-tags` - Allow all tags to be set via the PVC; even those used by the EBS controllers. Use with caution!

#### Annotations

`k8s-pvc-tagger/ignore` - When this annotation is set (any value) it will ignore this PVC and not add any tags to it

`k8s-pvc-tagger/tags` - A json encoded key/value map of the tags to set on the EBS Volume (in addition to the `--default-tags`). It can also be used to override the values set in the `--default-tags`

NOTE: Until version `v1.1.0` the legacy annotation prefix of `aws-ebs-tagger` will continue to be supported for aws-ebs volumes ONLY.

#### Examples

1. The cmdline arg `--default-tags={"me": "touge"}` and no annotation will set the tag `me=touge`

2. The cmdline arg `--default-tags={"me": "touge"}` and the annotation `k8s-pvc-tagger/tags: | {"me": "someone else", "another tag": "some value"}` will create the tags `me=someone else` and `another tag=some value` on the EBS Volume

3. The cmdline arg `--default-tags={"me": "touge"}` and the annotation `k8s-pvc-tagger/ignore: ""` will not set any tags on the EBS Volume

4. The cmdline arg `--default-tags={"me": "touge"}` and the annotation `k8s-pvc-tagger/tags: | {"cost-center": "abc", "environment": "prod"}` will create the tags `me=touge`, `cost-center=abc` and `environment=prod` on the EBS Volume

#### ignored tags

The following tags are ignored by default
 - `kubernetes.io/*`
 - `KubernetesCluster`
 - `Name`

#### Tag Templates

Tag values can be Go templates using values from the PVC's `Name`, `Namespace`, `Annotations`, and `Labels`.

Some examples could be:

```yaml
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: touge-test
  namespace: touge
  labels:
    TeamID: "Frontend"
  annotations:
    CostCenter: "1234"
    k8s-pvc-tagger/tags: |
      {"Owner": "{{ .Labels.TeamID }}-{{ .Annotations.CostCenter }}"}
---
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: app-1
  namespace: my-app
  annotations:
    k8s-pvc-tagger/tags: |
      {"OwnerID": "{{ .Namespace }}/{{ .Name }}"}
```

### Installation

#### AWS IAM Role

You need to create an AWS IAM Role that can be used by `k8s-pvc-tagger`. I recommend using a tool like [kube2iam](https://github.com/jtblin/kube2iam) instead of using an AWS access key/secret. An example policy is in [examples/iam-role.json](examples/iam-role.json).

#### Install via helm

```
helm repo add mtougeron https://mtougeron.github.io/helm-charts/
helm repo update
helm install k8s-pvc-tagger mtougeron/k8s-pvc-tagger
```

#### Container Image

Images are available on the [GitHub Container Registry](https://github.com/users/mtougeron/packages/container/k8s-pvc-tagger/versions) and [DockerHub](https://hub.docker.com/r/mtougeron/k8s-pvc-tagger). Containers are published for `linux/amd64` & `linux/arm64`.


### Licensing

This project is licensed under the Apache V2 License. See [LICENSE](https://github.com/mtougeron/k8s-pvc-tagger/blob/main/LICENSE) for more information.
