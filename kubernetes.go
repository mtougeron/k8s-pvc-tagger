// Licensed to Michael Tougeron <github@e.tougeron.com> under
// one or more contributor license agreements. See the LICENSE
// file distributed with this work for additional information
// regarding copyright ownership.
// Michael Tougeron <github@e.tougeron.com> licenses this file
// to you under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing,
// software distributed under the License is distributed on an
// "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY
// KIND, either express or implied.  See the License for the
// specific language governing permissions and limitations
// under the License.

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/service/fsx"
	"github.com/prometheus/client_golang/prometheus"
	log "github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	_ "k8s.io/client-go/plugin/pkg/client/auth"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"
)

var (
	// DefaultKubeConfigFile local kubeconfig if not running in cluster
	DefaultKubeConfigFile = filepath.Join(os.Getenv("HOME"), ".kube", "config")
	k8sClient             kubernetes.Interface
	awsVolumeRegMatch     = regexp.MustCompile("^vol-[^/]*$")
)

const (
	// Matching strings for volume operations.
	regexpEFSVolumeID = `^fs-\w+::(fsap-\w+)$`

	// supported AWS storage provisioners:
	AWS_EBS_CSI    = "ebs.csi.aws.com"
	AWS_EBS_LEGACY = "kubernetes.io/aws-ebs"
	AWS_EFS_CSI    = "efs.csi.aws.com"
	AWS_FSX_CSI    = "fsx.csi.aws.com"

	// supported AZURE storage provisioners:
	AZURE_DISK_CSI = "disk.csi.azure.com"

	// supported GCP storage provisioners:
	GCP_PD_CSI    = "pd.csi.storage.gke.io"
	GCP_PD_LEGACY = "kubernetes.io/gce-pd"
)

type TagTemplate struct {
	Name        string
	Namespace   string
	Labels      map[string]string
	Annotations map[string]string
}

func BuildClient(kubeconfig string, kubeContext string) (*kubernetes.Clientset, error) {
	config, err := rest.InClusterConfig()
	if err != nil {
		if kubeconfig == "" {
			kubeconfig = DefaultKubeConfigFile
		}
		config, err = buildConfigFromFlags(kubeconfig, kubeContext)
		if err != nil {
			return nil, err
		}
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		panic(err.Error())
	}
	return clientset, nil
}

func buildConfigFromFlags(kubeconfig string, context string) (*rest.Config, error) {
	return clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		&clientcmd.ClientConfigLoadingRules{ExplicitPath: kubeconfig},
		&clientcmd.ConfigOverrides{
			CurrentContext: context,
		}).ClientConfig()
}

func watchForPersistentVolumeClaims(ch chan struct{}, watchNamespace string) {
	var err error
	var factory informers.SharedInformerFactory
	log.WithFields(log.Fields{"namespace": watchNamespace}).Infoln("Starting informer")
	if watchNamespace == "" {
		factory = informers.NewSharedInformerFactory(k8sClient, 0)
	} else {
		factory = informers.NewSharedInformerFactoryWithOptions(k8sClient, 0, informers.WithNamespace(watchNamespace))
	}

	informer := factory.Core().V1().PersistentVolumeClaims().Informer()

	var efsClient *EFSClient
	var ec2Client *EBSClient
	var fsxClient *FSxClient
	var gcpClient GCPClient
	var azureClient AzureClient

	switch cloud {
	case AWS:
		efsClient, _ = newEFSClient()
		ec2Client, _ = newEC2Client()
		fsxClient, _ = newFSxClient()
	case AZURE:
		// see how to get the credentials with a service account and the subscription
		azureClient, err = NewAzureClient()
		if err != nil {
			log.Fatalln("failed to create Azure client", err)
		}
	case GCP:
		gcpClient, err = newGCPClient(context.Background())
		if err != nil {
			log.Fatalln("failed to create GCP client", err)
		}
	}

	_, err = informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			pvc := getPVC(obj)
			log.WithFields(log.Fields{"namespace": pvc.GetNamespace(), "pvc": pvc.GetName()}).Infoln("New PVC Added to Store")

			volumeID, tags, err := processPersistentVolumeClaim(pvc)
			if err != nil || len(tags) == 0 {
				return
			}

			switch cloud {
			case AWS:
				if !provisionedByAwsEfs(pvc) && !provisionedByAwsEbs(pvc) && !provisionedByAwsFsx(pvc) {
					return
				}

				if provisionedByAwsEfs(pvc) {
					efsClient.addEFSVolumeTags(volumeID, tags, *pvc.Spec.StorageClassName)
				}
				if provisionedByAwsEbs(pvc) {
					ec2Client.addEBSVolumeTags(volumeID, tags, *pvc.Spec.StorageClassName)
				}
				if provisionedByAwsFsx(pvc) {
					fsxClient.addFSxVolumeTags(volumeID, tags, *pvc.Spec.StorageClassName)
				}
			case AZURE:
				if provisionedByAzureDisk(pvc) {
					err = UpdateAzureVolumeTags(context.Background(), azureClient, volumeID, tags, []string{}, *pvc.Spec.StorageClassName)
					if err != nil {
						log.WithFields(log.Fields{"namespace": pvc.GetNamespace(), "pvc": pvc.GetName(), "error": err.Error()}).Error("failed to update persistent volume")
					}
				}

			case GCP:
				if !provisionedByGcpPD(pvc) {
					return
				}
				addPDVolumeLabels(gcpClient, volumeID, tags, *pvc.Spec.StorageClassName)
			}
		},

		UpdateFunc: func(old, new interface{}) {
			newPVC := getPVC(new)
			oldPVC := getPVC(old)
			if newPVC.ResourceVersion == oldPVC.ResourceVersion {
				log.WithFields(log.Fields{"namespace": newPVC.GetNamespace(), "pvc": newPVC.GetName()}).Debugln("ResourceVersion are the same")
				return
			}
			if newPVC.Spec.VolumeName == "" {
				log.WithFields(log.Fields{"namespace": newPVC.GetNamespace(), "pvc": newPVC.GetName()}).Debugln("PersistentVolume not created yet")
				return
			}
			if newPVC.GetDeletionTimestamp() != nil {
				log.WithFields(log.Fields{"namespace": newPVC.GetNamespace(), "pvc": newPVC.GetName()}).Debugln("PersistentVolumeClaim is being deleted")
				return
			}
			log.WithFields(log.Fields{"namespace": newPVC.GetNamespace(), "pvc": newPVC.GetName()}).Infoln("Need to reconcile tags")

			volumeID, tags, err := processPersistentVolumeClaim(newPVC)
			if err != nil {
				return
			}

			switch cloud {
			case AWS:
				if !provisionedByAwsEfs(newPVC) && !provisionedByAwsEbs(newPVC) && !provisionedByAwsFsx(newPVC) {
					return
				}

				if len(tags) > 0 {
					if provisionedByAwsEfs(newPVC) {
						efsClient.addEFSVolumeTags(volumeID, tags, *newPVC.Spec.StorageClassName)
					}
					if provisionedByAwsEbs(newPVC) {
						ec2Client.addEBSVolumeTags(volumeID, tags, *newPVC.Spec.StorageClassName)
					}
					if provisionedByAwsFsx(newPVC) {
						fsxClient.addFSxVolumeTags(volumeID, tags, *newPVC.Spec.StorageClassName)
					}
				}
				oldTags := buildTags(oldPVC)
				var deletedTags []string
				var deletedTagsPtr []*string
				for k := range oldTags {
					if _, ok := tags[k]; !ok {
						deletedTags = append(deletedTags, k)
						deletedTagsPtr = append(deletedTagsPtr, &k)
					}
				}
				if len(deletedTags) > 0 {
					if provisionedByAwsEfs(newPVC) {
						efsClient.deleteEFSVolumeTags(volumeID, deletedTags, *oldPVC.Spec.StorageClassName)
					}
					if provisionedByAwsEbs(newPVC) {
						ec2Client.deleteEBSVolumeTags(volumeID, deletedTags, *oldPVC.Spec.StorageClassName)
					}
					if provisionedByAwsFsx(newPVC) {
						fsxClient.deleteFSxVolumeTags(volumeID, deletedTagsPtr, *oldPVC.Spec.StorageClassName)
					}
				}
			case AZURE:
				if !provisionedByAzureDisk(newPVC) {
					var deletedTags []string
					oldTags := buildTags(oldPVC)
					for k := range oldTags {
						if _, ok := tags[k]; !ok {
							deletedTags = append(deletedTags, k)
						}
					}
					err := UpdateAzureVolumeTags(context.Background(), azureClient, volumeID, tags, deletedTags, *newPVC.Spec.StorageClassName)
					if err != nil {
						log.WithFields(log.Fields{"namespace": newPVC.GetNamespace(), "pvc": newPVC.GetName()}).Error("failed to update persistent volume")
					}
				}
				return
			case GCP:
				if !provisionedByGcpPD(newPVC) {
					return
				}

				if len(tags) > 0 {
					addPDVolumeLabels(gcpClient, volumeID, tags, *newPVC.Spec.StorageClassName)
				}
				oldTags := buildTags(oldPVC)
				var deletedTags []string
				for k := range oldTags {
					if _, ok := tags[k]; !ok {
						deletedTags = append(deletedTags, k)
					}
				}
				if len(deletedTags) > 0 {
					deletePDVolumeLabels(gcpClient, volumeID, deletedTags, *newPVC.Spec.StorageClassName)
				}
			}
		},
	})
	if err != nil {
		log.Errorln("Can't setup PVC informer! Check RBAC permissions")
		return
	}

	informer.Run(ch)
}

func provisionedByAzureDisk(pvc *corev1.PersistentVolumeClaim) bool {
	annotations := pvc.GetAnnotations()
	if annotations == nil {
		return false
	}

	provisionedBy, ok := getProvisionedBy(annotations)
	if !ok {
		log.WithFields(log.Fields{"namespace": pvc.GetNamespace(), "pvc": pvc.GetName()}).Debugln("no volume.kubernetes.io/storage-provisioner annotation")
		return false
	}

	switch provisionedBy {
	case AZURE_DISK_CSI:
		log.WithFields(log.Fields{"namespace": pvc.GetNamespace(), "pvc": pvc.GetName()}).Debugln(AWS_EBS_LEGACY + " volume")
		return true
	}
	return false
}

func convertTagsToFSxTags(tags map[string]string) []*fsx.Tag {
	convertedTags := []*fsx.Tag{}
	for tagKey, tagValue := range tags {
		convertedTags = append(convertedTags, &fsx.Tag{
			Key:   aws.String(tagKey),
			Value: aws.String(tagValue),
		})
	}
	return convertedTags
}

func parseAWSEBSVolumeID(kubernetesID string) string {
	// Pulled from https://github.com/kubernetes/csi-translation-lib/blob/release-1.26/plugins/aws_ebs.go#L244
	if !strings.HasPrefix(kubernetesID, "aws://") {
		// Assume a bare aws volume id (vol-1234...)
		return kubernetesID
	}
	url, err := url.Parse(kubernetesID)
	if err != nil {
		log.Errorln(fmt.Sprintf("Invalid disk name (%s): %v", kubernetesID, err))
		return ""
	}
	if url.Scheme != "aws" {
		log.Errorln(fmt.Sprintf("Invalid scheme for AWS volume (%s)", kubernetesID))
		return ""
	}
	awsID := url.Path
	awsID = strings.Trim(awsID, "/")

	if !awsVolumeRegMatch.MatchString(awsID) {
		log.Errorln(fmt.Sprintf("Invalid format for AWS volume (%s)", kubernetesID))
		return ""
	}

	return awsID
}

func parseAWSEFSVolumeID(k8sVolumeID string) string {
	re := regexp.MustCompile(regexpEFSVolumeID)
	matches := re.FindSubmatch([]byte(k8sVolumeID))
	if len(matches) <= 1 {
		log.Errorln("Can't parse valid AWS EFS volumeID:", k8sVolumeID)
		return ""
	}
	return string(matches[1])
}

func buildTags(pvc *corev1.PersistentVolumeClaim) map[string]string {
	tags := map[string]string{}
	customTags := map[string]string{}
	var tagString string
	var legacyTagString string

	annotations := pvc.GetAnnotations()
	// Skip if the annotation says to ignore this PVC
	if _, ok := annotations[annotationPrefix+"/ignore"]; ok {
		log.Debugln(annotationPrefix + "/ignore annotation is set")
		promIgnoredTotal.With(prometheus.Labels{"storageclass": *pvc.Spec.StorageClassName}).Inc()
		promIgnoredLegacyTotal.Inc()
		return renderTagTemplates(pvc, tags)
	}
	// if the annotationPrefix has been changed, then we don't compare to the legacyAnnotationPrefix anymore
	if annotationPrefix == defaultAnnotationPrefix {
		if _, ok := annotations[legacyAnnotationPrefix+"/ignore"]; ok {
			log.Debugln(legacyAnnotationPrefix + "/ignore annotation is set")
			promIgnoredTotal.With(prometheus.Labels{"storageclass": *pvc.Spec.StorageClassName}).Inc()
			promIgnoredLegacyTotal.Inc()
			return renderTagTemplates(pvc, tags)
		}
	}

	// Set the default tags
	for k, v := range defaultTags {
		if !isValidTagName(k) {
			if !allowAllTags {
				log.Warnln(k, "is a restricted tag. Skipping...")
				promInvalidTagsTotal.With(prometheus.Labels{"storageclass": *pvc.Spec.StorageClassName}).Inc()
				promInvalidTagsLegacyTotal.Inc()
				continue
			} else {
				log.Warnln(k, "is a restricted tag but still allowing it to be set...")
			}
		}
		tags[k] = v
	}

	if len(copyLabels) > 0 {
		for k, v := range pvc.GetLabels() {
			if copyLabels[0] == "*" || slices.Contains(copyLabels, k) {
				if !isValidTagName(k) {
					if !allowAllTags {
						log.Warnln(k, "is a restricted tag. Skipping...")
						promInvalidTagsTotal.With(prometheus.Labels{"storageclass": *pvc.Spec.StorageClassName}).Inc()
						promInvalidTagsLegacyTotal.Inc()
						continue
					} else {
						log.Warnln(k, "is a restricted tag but still allowing it to be set...")
					}
				}
				tags[k] = v
			}
		}
	}

	var legacyOk bool
	tagString, ok := annotations[annotationPrefix+"/tags"]
	// if the annotationPrefix has been changed, then we don't compare to the legacyAnnotationPrefix anymore
	if annotationPrefix == defaultAnnotationPrefix {
		legacyTagString, legacyOk = annotations[legacyAnnotationPrefix+"/tags"]
	} else {
		legacyOk = false
		legacyTagString = ""
	}
	if !ok && !legacyOk {
		log.Debugln("Does not have " + annotationPrefix + "/tags or legacy " + legacyAnnotationPrefix + "/tags annotation")
		return renderTagTemplates(pvc, tags)
	} else if ok && legacyOk {
		log.Warnln("Has both " + annotationPrefix + "/tags AND legacy " + legacyAnnotationPrefix + "/tags annotation. Using newer " + annotationPrefix + "/tags annotation")
	} else if legacyOk && !ok {
		tagString = legacyTagString
	}
	if tagFormat == "csv" {
		customTags = parseCsv(tagString)
	} else {
		err := json.Unmarshal([]byte(tagString), &customTags)
		if err != nil {
			log.Errorln("Failed to Unmarshal JSON:", err)
		}
	}

	for k, v := range customTags {
		if !isValidTagName(k) {
			if !allowAllTags {
				log.Warnln(k, "is a restricted tag. Skipping...")
				promInvalidTagsTotal.With(prometheus.Labels{"storageclass": *pvc.Spec.StorageClassName}).Inc()
				promInvalidTagsLegacyTotal.Inc()
				continue
			} else {
				log.Warnln(k, "is a restricted tag but still allowing it to be set...")
			}
		}
		tags[k] = v
	}

	return renderTagTemplates(pvc, tags)
}

func renderTagTemplates(pvc *corev1.PersistentVolumeClaim, tags map[string]string) map[string]string {
	tplData := TagTemplate{
		Name:        pvc.GetName(),
		Namespace:   pvc.GetNamespace(),
		Labels:      pvc.GetLabels(),
		Annotations: pvc.GetAnnotations(),
	}

	for k, v := range tags {
		tmpl, err := template.New("tag").Parse(v)
		if err != nil {
			continue
		}
		buf := new(bytes.Buffer)
		err = tmpl.Execute(buf, tplData)
		if err != nil {
			continue
		}
		tags[k] = buf.String()
	}

	return tags
}

func isValidTagName(name string) bool {
	if strings.HasPrefix(strings.ToLower(name), "kubernetes.io") {
		return false
	} else if strings.ToLower(name) == "name" {
		return false
	} else if strings.ToLower(name) == "kubernetescluster" {
		return false
	}

	return true
}

func provisionedByAwsEfs(pvc *corev1.PersistentVolumeClaim) bool {
	annotations := pvc.GetAnnotations()
	if annotations == nil {
		return false
	}

	provisionedBy, ok := getProvisionedBy(annotations)
	if !ok {
		log.WithFields(log.Fields{"namespace": pvc.GetNamespace(), "pvc": pvc.GetName()}).Debugln("no volume.kubernetes.io/storage-provisioner annotation")
		return false
	}

	if provisionedBy == AWS_EFS_CSI {
		log.WithFields(log.Fields{"namespace": pvc.GetNamespace(), "pvc": pvc.GetName()}).Debugln(AWS_EFS_CSI + " volume")
		return true
	}
	return false
}

func provisionedByAwsEbs(pvc *corev1.PersistentVolumeClaim) bool {
	annotations := pvc.GetAnnotations()
	if annotations == nil {
		return false
	}

	provisionedBy, ok := getProvisionedBy(annotations)
	if !ok {
		log.WithFields(log.Fields{"namespace": pvc.GetNamespace(), "pvc": pvc.GetName()}).Debugln("no volume.kubernetes.io/storage-provisioner annotation")
		return false
	}

	switch provisionedBy {
	case AWS_EBS_LEGACY:
		log.WithFields(log.Fields{"namespace": pvc.GetNamespace(), "pvc": pvc.GetName()}).Debugln(AWS_EBS_LEGACY + " volume")
		return true
	case AWS_EBS_CSI:
		log.WithFields(log.Fields{"namespace": pvc.GetNamespace(), "pvc": pvc.GetName()}).Debugln(AWS_EBS_CSI + " volume")
		return true
	}
	return false
}

func provisionedByAwsFsx(pvc *corev1.PersistentVolumeClaim) bool {
	annotations := pvc.GetAnnotations()
	if annotations == nil {
		return false
	}

	provisionedBy, ok := getProvisionedBy(annotations)
	if !ok {
		log.WithFields(log.Fields{"namespace": pvc.GetNamespace(), "pvc": pvc.GetName()}).Debugln("no volume.kubernetes.io/storage-provisioner annotation")
		return false
	}

	if provisionedBy == AWS_FSX_CSI {
		log.WithFields(log.Fields{"namespace": pvc.GetNamespace(), "pvc": pvc.GetName()}).Debugln(AWS_FSX_CSI + " volume")
		return true
	}
	return false
}

func provisionedByGcpPD(pvc *corev1.PersistentVolumeClaim) bool {
	annotations := pvc.GetAnnotations()
	if annotations == nil {
		return false
	}

	provisionedBy, ok := getProvisionedBy(annotations)
	if !ok {
		log.WithFields(log.Fields{"namespace": pvc.GetNamespace(), "pvc": pvc.GetName()}).Debugln("no volume.kubernetes.io/storage-provisioner annotation")
		return false
	}

	switch provisionedBy {
	case GCP_PD_LEGACY:
		log.WithFields(log.Fields{"namespace": pvc.GetNamespace(), "pvc": pvc.GetName()}).Debugln(GCP_PD_LEGACY + " volume")
		return true
	case GCP_PD_CSI:
		log.WithFields(log.Fields{"namespace": pvc.GetNamespace(), "pvc": pvc.GetName()}).Debugln(GCP_PD_CSI + " volume")
		return true
	}
	return false
}

func processPersistentVolumeClaim(pvc *corev1.PersistentVolumeClaim) (string, map[string]string, error) {
	tags := buildTags(pvc)

	log.WithFields(log.Fields{"namespace": pvc.GetNamespace(), "pvc": pvc.GetName(), "tags": tags}).Debugln("PVC Tags")

	pv, err := k8sClient.CoreV1().PersistentVolumes().Get(context.TODO(), pvc.Spec.VolumeName, metav1.GetOptions{})
	if err != nil {
		log.WithFields(log.Fields{"namespace": pvc.GetNamespace(), "pvc": pvc.GetName()}).Errorln("Get PV from kubernetes cluster error:", err)
		return "", nil, err
	}

	var volumeID string
	annotations := pvc.GetAnnotations()
	if annotations == nil {
		log.Errorf("cannot get PVC annotations")
		return "", nil, errors.New("cannot get PVC annotations")
	}

	provisionedBy, ok := getProvisionedBy(annotations)
	if !ok {
		log.Errorf("cannot get volume.kubernetes.io/storage-provisioner annotation")
		return "", nil, errors.New("cannot get volume.kubernetes.io/storage-provisioner annotation")
	}

	switch provisionedBy {
	case AWS_EBS_CSI:
		if pv.Spec.CSI != nil {
			volumeID = pv.Spec.CSI.VolumeHandle
		} else {
			volumeID = parseAWSEBSVolumeID(pv.Spec.AWSElasticBlockStore.VolumeID)
		}
	case AWS_EFS_CSI:
		if pv.Spec.CSI != nil {
			volumeID = parseAWSEFSVolumeID(pv.Spec.CSI.VolumeHandle)
		}
	case AWS_EBS_LEGACY:
		volumeID = parseAWSEBSVolumeID(pv.Spec.AWSElasticBlockStore.VolumeID)
	case AWS_FSX_CSI:
		volumeID = pv.Spec.CSI.VolumeHandle
	case GCP_PD_LEGACY:
		volumeID = pv.Spec.GCEPersistentDisk.PDName
	case AZURE_DISK_CSI:
		volumeID = pv.Spec.CSI.VolumeHandle
	case GCP_PD_CSI:
		volumeID = pv.Spec.CSI.VolumeHandle
	}

	log.WithFields(log.Fields{"namespace": pvc.GetNamespace(), "pvc": pvc.GetName(), "volumeID": volumeID}).Debugln("parsed volumeID:", volumeID)
	if len(volumeID) == 0 {
		log.Errorf("Cannot parse VolumeID")
		return "", nil, errors.New("cannot parse VolumeID")
	}

	return volumeID, tags, nil
}

func getCurrentNamespace() string {
	// Fall back to the namespace associated with the service account token, if available
	if data, err := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/namespace"); err == nil {
		if ns := strings.TrimSpace(string(data)); len(ns) > 0 {
			return ns
		}
	}

	return ""
}

func getProvisionedBy(annotations map[string]string) (string, bool) {
	var provisionedBy string
	provisionedBy, ok := annotations["volume.kubernetes.io/storage-provisioner"]
	if !ok {
		provisionedBy, ok = annotations["volume.beta.kubernetes.io/storage-provisioner"]
	}

	return provisionedBy, ok
}

func getPVC(obj interface{}) *corev1.PersistentVolumeClaim {
	pvc := obj.(*corev1.PersistentVolumeClaim)

	// https://kubernetes.io/docs/reference/labels-annotations-taints/#volume-beta-kubernetes-io-storage-class-deprecated
	// The "volume.beta.kubernetes.io/storage-class" annotation is deprecated but can be used
	// to specify the name of StorageClass in PVC. When both storageClassName attribute and
	// volume.beta.kubernetes.io/storage-class annotation are specified, the annotation
	// volume.beta.kubernetes.io/storage-class takes precedence over the storageClassName attribute.
	storageClassName, ok := pvc.GetAnnotations()["volume.beta.kubernetes.io/storage-class"]
	if ok {
		pvc.Spec.StorageClassName = &storageClassName
	}

	return pvc
}
