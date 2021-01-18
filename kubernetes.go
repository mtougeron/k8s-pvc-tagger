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
	"context"
	"encoding/json"
	"errors"
	"io/ioutil"
	"os"
	"path/filepath"
	"regexp"
	"strings"

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
)

const (
	// Matching strings for volume operations.
	regexpAWSVolumeID = `^aws:\/\/\w{2}-\w{4,9}-\d\w\/(vol-\w+)$`
)

func buildClient(kubeconfig string, kubeContext string) (*kubernetes.Clientset, error) {
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

func watchForPersistentVolumeClaims(watchNamespace string) {

	var factory informers.SharedInformerFactory
	log.WithFields(log.Fields{"namespace": watchNamespace}).Infoln("Starting informer")
	if watchNamespace == "" {
		factory = informers.NewSharedInformerFactory(k8sClient, 0)
	} else {
		factory = informers.NewSharedInformerFactoryWithOptions(k8sClient, 0, informers.WithNamespace(watchNamespace))
	}

	informer := factory.Core().V1().PersistentVolumeClaims().Informer()
	stopper := make(chan struct{})
	defer close(stopper)

	ec2Client, _ := newEC2Client()

	informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			pvc := obj.(*corev1.PersistentVolumeClaim)
			if !provisionedByAwsEbs(pvc) {
				return
			}
			log.WithFields(log.Fields{"namespace": pvc.GetNamespace(), "pvc": pvc.GetName()}).Infoln("New PVC Added to Store")

			volumeID, tags, err := processPersistentVolumeClaim(pvc)
			if err != nil || len(tags) == 0 {
				return
			}
			ec2Client.addVolumeTags(volumeID, tags)
		},
		UpdateFunc: func(old, new interface{}) {

			newPVC := new.(*corev1.PersistentVolumeClaim)
			oldPVC := old.(*corev1.PersistentVolumeClaim)
			if newPVC.ResourceVersion == oldPVC.ResourceVersion {
				log.WithFields(log.Fields{"namespace": newPVC.GetNamespace(), "pvc": newPVC.GetName()}).Debugln("ResourceVersion are the same")
				return
			}
			if !provisionedByAwsEbs(newPVC) {
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
			if len(tags) > 0 {
				ec2Client.addVolumeTags(volumeID, tags)
			}

			oldTags := buildTags(oldPVC)
			var deletedTags []string
			for k := range oldTags {
				if _, ok := tags[k]; !ok {
					deletedTags = append(deletedTags, k)
				}
			}
			if len(deletedTags) > 0 {
				ec2Client.deleteVolumeTags(volumeID, deletedTags)
			}
		},
	})

	informer.Run(stopper)
}

func parseAWSVolumeID(k8sVolumeID string) string {
	re := regexp.MustCompile(regexpAWSVolumeID)
	matches := re.FindSubmatch([]byte(k8sVolumeID))
	if len(matches) <= 1 {
		log.Errorln("Can't parse valid AWS EBS volumeID:", k8sVolumeID)
		return ""
	}
	return string(matches[1])
}

func buildTags(pvc *corev1.PersistentVolumeClaim) map[string]string {

	tags := map[string]string{}
	customTags := map[string]string{}
	var tagString string

	annotations := pvc.GetAnnotations()
	// Skip if the annotation says to ignore this PVC
	if _, ok := annotations[annotationPrefix+"/ignore"]; ok {
		log.Debugln(annotationPrefix + "/ignore annotation is set")
		promIgnoredTotal.Inc()
		return tags
	}

	// Set the default tags
	for k, v := range defaultTags {
		if !isValidTagName(k) {
			log.Warnln(k, "is a restricted tag. Skipping...")
			promInvalidTagsTotal.Inc()
			continue
		}
		tags[k] = v
	}

	tagString, ok := annotations[annotationPrefix+"/tags"]
	if !ok {
		log.Debugln("Does not have " + annotationPrefix + "/tags annotation")
		return tags
	}
	err := json.Unmarshal([]byte(tagString), &customTags)
	if err != nil {
		log.Errorln("Failed to Unmarshal JSON:", err)
	}

	for k, v := range customTags {
		if !isValidTagName(k) {
			log.Warnln(k, "is a restricted tag. Skipping...")
			promInvalidTagsTotal.Inc()
			continue
		}
		tags[k] = v
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
	if pvc.GetAnnotations()["volume.beta.kubernetes.io/storage-provisioner"] == "ebs.csi.aws.com" {
		volumeID = pv.Spec.CSI.VolumeHandle
	} else if pvc.GetAnnotations()["volume.beta.kubernetes.io/storage-provisioner"] == "kubernetes.io/aws-ebs" {
		volumeID = parseAWSVolumeID(pv.Spec.PersistentVolumeSource.AWSElasticBlockStore.VolumeID)
	}
	log.WithFields(log.Fields{"namespace": pvc.GetNamespace(), "pvc": pvc.GetName(), "volumeID": volumeID}).Debugln("parsed volumeID:", volumeID)
	if len(volumeID) == 0 {
		log.Errorf("Cannot parse VolumeID")
		return "", nil, errors.New("Cannot parse VolumeID")
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
