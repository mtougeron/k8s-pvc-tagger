# k8s-aws-ebs-tagger
A utility to tag AWS EBS volumes based on the PV labels / annotations

![Go](https://github.com/mtougeron/k8s-aws-ebs-tagger/workflows/Go/badge.svg) ![Gosec](https://github.com/mtougeron/k8s-aws-ebs-tagger/workflows/Gosec/badge.svg) ![ContainerScan](https://github.com/mtougeron/k8s-aws-ebs-tagger/workflows/ContainerScan/badge.svg) [![GitHub tag](https://img.shields.io/github/tag/mtougeron/k8s-aws-ebs-tagger.svg)](https://github.com/mtougeron/k8s-aws-ebs-tagger/tags/)

The `k8s-aws-ebs-tagger` watches for new PersistentVolumes and when new AWS EBS volumes are created it adds tags based on the PV labels to the created EBS volume.

#### Container Image

Images are available on the [GitHub Container Registry](https://github.com/users/mtougeron/packages/container/k8s-aws-ebs-tagger/versions) and [DockerHub](https://hub.docker.com/repository/docker/mtougeron/k8s-aws-ebs-tagger)

### Licensing

This project is licensed under the Apache V2 License. See [LICENSE](LICENSE) for more information.
