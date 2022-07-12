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
	"github.com/prometheus/client_golang/prometheus"
	log "github.com/sirupsen/logrus"
	"html/template"
	"io/ioutil"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	_ "k8s.io/client-go/plugin/pkg/client/auth"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var (
	// DefaultKubeConfigFile local kubeconfig if not running in cluster
	DefaultKubeConfigFile = filepath.Join(os.Getenv("HOME"), ".kube", "config")
	k8sClient             kubernetes.Interface
)

const (
	// Matching strings for volume operations.
	regexpAWSVolumeID = `^aws:\/\/\w{2}-\w{4,9}-\d\w\/(vol-\w+)$`
	regexpEFSVolumeID = `^fs-\w+::(fsap-\w+)$`
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

	var factory informers.SharedInformerFactory
	log.WithFields(log.Fields{"namespace": watchNamespace}).Infoln("Starting informer")
	if watchNamespace == "" {
		factory = informers.NewSharedInformerFactory(k8sClient, 0)
	} else {
		factory = informers.NewSharedInformerFactoryWithOptions(k8sClient, 0, informers.WithNamespace(watchNamespace))
	}

	informer := factory.Core().V1().PersistentVolumeClaims().Informer()

	efsClient, _ := newEFSClient()
	ec2Client, _ := newEC2Client()

	informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			pvc := obj.(*corev1.PersistentVolumeClaim)
			if !provisionedByAwsEfs(pvc) && !provisionedByAwsEbs(pvc) {
				return
			}
			log.WithFields(log.Fields{"namespace": pvc.GetNamespace(), "pvc": pvc.GetName()}).Infoln("New PVC Added to Store")

			volumeID, tags, err := processPersistentVolumeClaim(pvc)
			if err != nil || len(tags) == 0 {
				return
			}
			if provisionedByAwsEfs(pvc) {
				efsClient.addEFSVolumeTags(parseAWSEFSVolumeID(volumeID), tags, *pvc.Spec.StorageClassName)
			}
			if provisionedByAwsEbs(pvc) {
				ec2Client.addEBSVolumeTags(volumeID, tags, *pvc.Spec.StorageClassName)
			}
		},
		UpdateFunc: func(old, new interface{}) {

			newPVC := new.(*corev1.PersistentVolumeClaim)
			oldPVC := old.(*corev1.PersistentVolumeClaim)
			if newPVC.ResourceVersion == oldPVC.ResourceVersion {
				log.WithFields(log.Fields{"namespace": newPVC.GetNamespace(), "pvc": newPVC.GetName()}).Debugln("ResourceVersion are the same")
				return
			}
			if !provisionedByAwsEfs(newPVC) && !provisionedByAwsEbs(newPVC) {
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
			if err != nil || len(tags) == 0 {
				return
			}

			if provisionedByAwsEfs(newPVC) {
				efsClient.addEFSVolumeTags(parseAWSEFSVolumeID(volumeID), tags, *newPVC.Spec.StorageClassName)
			}
			if provisionedByAwsEbs(newPVC) {
				ec2Client.addEBSVolumeTags(volumeID, tags, *newPVC.Spec.StorageClassName)
			}

			oldTags := buildTags(oldPVC)
			var deletedTags []string
			for k := range oldTags {
				if _, ok := tags[k]; !ok {
					deletedTags = append(deletedTags, k)
				}
			}
			if len(deletedTags) > 0 {
				if provisionedByAwsEfs(newPVC) {
					efsClient.deleteEFSVolumeTags(parseAWSEFSVolumeID(volumeID), deletedTags, *oldPVC.Spec.StorageClassName)
				}
				if provisionedByAwsEbs(newPVC) {
					ec2Client.deleteEBSVolumeTags(volumeID, deletedTags, *oldPVC.Spec.StorageClassName)
				}
			}
		},
	})

	informer.Run(ch)
}

func parseAWSEBSVolumeID(k8sVolumeID string) string {
	re := regexp.MustCompile(regexpAWSVolumeID)
	matches := re.FindSubmatch([]byte(k8sVolumeID))
	if len(matches) <= 1 {
		log.Errorln("Can't parse valid AWS EBS volumeID:", k8sVolumeID)
		return ""
	}
	return string(matches[1])
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
	if provisionedBy, ok := annotations["volume.beta.kubernetes.io/storage-provisioner"]; !ok {
		log.WithFields(log.Fields{"namespace": pvc.GetNamespace(), "pvc": pvc.GetName()}).Debugln("no volume.beta.kubernetes.io/storage-provisioner annotation")
		return false
	} else if provisionedBy == "efs.csi.aws.com" {
		log.WithFields(log.Fields{"namespace": pvc.GetNamespace(), "pvc": pvc.GetName()}).Debugln("efs.csi.aws.com volume")
		return true
	}
	return false
}

func provisionedByAwsEbs(pvc *corev1.PersistentVolumeClaim) bool {
	annotations := pvc.GetAnnotations()
	if provisionedBy, ok := annotations["volume.beta.kubernetes.io/storage-provisioner"]; !ok {
		log.WithFields(log.Fields{"namespace": pvc.GetNamespace(), "pvc": pvc.GetName()}).Debugln("no volume.beta.kubernetes.io/storage-provisioner annotation")
		return false
	} else if provisionedBy == "kubernetes.io/aws-ebs" {
		log.WithFields(log.Fields{"namespace": pvc.GetNamespace(), "pvc": pvc.GetName()}).Debugln("kubernetes.io/aws-ebs volume")
		return true
	} else if provisionedBy == "ebs.csi.aws.com" {
		log.WithFields(log.Fields{"namespace": pvc.GetNamespace(), "pvc": pvc.GetName()}).Debugln("ebs.csi.aws.com volume")
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
	if provisionedBy, ok := annotations["volume.beta.kubernetes.io/storage-provisioner"]; !ok {
		log.Errorf("cannot get volume.beta.kubernetes.io/storage-provisioner annotation")
		return "", nil, errors.New("cannot get volume.beta.kubernetes.io/storage-provisioner annotation")
	} else if provisionedBy == "efs.csi.aws.com" || provisionedBy == "ebs.csi.aws.com" {
		volumeID = pv.Spec.PersistentVolumeSource.CSI.VolumeHandle
	} else if provisionedBy == "kubernetes.io/aws-ebs" {
		volumeID = parseAWSEBSVolumeID(pv.Spec.PersistentVolumeSource.AWSElasticBlockStore.VolumeID)
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
	if data, err := ioutil.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/namespace"); err == nil {
		if ns := strings.TrimSpace(string(data)); len(ns) > 0 {
			return ns
		}
	}

	return ""
}
