# k8s-aws-ebs-tagger

A utility to tag AWS EBS volumes based on the PVC's `aws-ebs-tagger/tags` annotation

![Go](https://github.com/mtougeron/k8s-aws-ebs-tagger/workflows/Go/badge.svg) ![Gosec](https://github.com/mtougeron/k8s-aws-ebs-tagger/workflows/Gosec/badge.svg) ![ContainerScan](https://github.com/mtougeron/k8s-aws-ebs-tagger/workflows/ContainerScan/badge.svg) [![GitHub tag](https://img.shields.io/github/tag/mtougeron/k8s-aws-ebs-tagger.svg)](https://github.com/mtougeron/k8s-aws-ebs-tagger/tags/)

The `k8s-aws-ebs-tagger` watches for new PersistentVolumeClaims and when new AWS EBS volumes are created it adds tags based on the PVC's `aws-ebs-tagger/tags` annotation to the created EBS volume.

### How to set tags

#### cmdline args

`--default-tags` - A json encoded key/value map of the tags to set by default on EBS Volumes. Values can be overwritten by the `aws-ebs-tagger/tags` annotation.

#### Annotations

`aws-ebs-tagger/ignore` - When this annotation is set (any value) it will ignore this PVC and not add any tags to it
`aws-ebs-tagger/tags` - A json encoded key/value map of the tags to set on the EBS Volume (in addition to the `--default-tags`). It can also be used to override the values set in the `--default-tags`

#### Examples

1. The cmdline arg `--default-tags={"me": "touge"}` and no annotation will set the tag `me=touge`

2. The cmdline arg `--default-tags={"me": "touge"}` and the annotation `aws-ebs-tagger/tags: | {"me": "someone else", "another tag": "some value"}` will create the tags `me=someone else` and `another tag=some value` on the EBS Volume

3. The cmdline arg `--default-tags={"me": "touge"}` and the annotation `aws-ebs-tagger/ignore: ""` will not set any tags on the EBS Volume

4. The cmdline arg `--default-tags={"me": "touge"}` and the annotation `aws-ebs-tagger/tags: | {"cost-center": "abc", "environment": "prod"}` will create the tags `me=touge`, `cost-center=abc` and `environment=prod` on the EBS Volume

#### ignored tags

The following tags are ignored
 - `kubernetes.io/*`
 - `KubernetesCluster`
 - `Name`

### Installation

#### AWS IAM Role

You need to create an AWS IAM Role that can be used by `k8s-aws-ebs-tagger`. I recommend using a tool like [kube2iam](https://github.com/jtblin/kube2iam) instead of using an AWS access key/secret. An example policy is in [examples/iam-role.json](examples/iam-role.json).

#### Install via helm

```
helm repo add mtougeron https://mtougeron.github.io/helm-charts/
helm repo update
helm install k8s-aws-ebs-tagger mtougeron/k8s-aws-ebs-tagger
```

#### Container Image

Images are available on the [GitHub Container Registry](https://github.com/users/mtougeron/packages/container/k8s-aws-ebs-tagger/versions) and [DockerHub](https://hub.docker.com/repository/docker/mtougeron/k8s-aws-ebs-tagger). Containers are published for `linux/amd64` & `linux/arm64`.

### Known Issues

It is currently only possible ([#9](https://github.com/mtougeron/k8s-aws-ebs-tagger/issues/9)) to watch all namespaces or a single namespace. This will be addressed in a future version of the app.

### Licensing

This project is licensed under the Apache V2 License. See [LICENSE](https://github.com/mtougeron/k8s-aws-ebs-tagger/blob/main/LICENSE) for more information.
