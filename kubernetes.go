package main

import (
	"context"
	"encoding/json"
	"errors"
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
	k8sClient             *kubernetes.Clientset
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

func watchForPersistentVolumeClaims() {
	factory := informers.NewSharedInformerFactory(k8sClient, 0)
	informer := factory.Core().V1().PersistentVolumeClaims().Informer()
	stopper := make(chan struct{})
	defer close(stopper)

	ec2Client, _ := newEC2Client()

	informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			mObj := obj.(metav1.Object)
			if !provisionedByAwsEbs(mObj) {
				return
			}
			log.Infoln("New PVC Added to Store:", mObj.GetName())

			pvc := obj.(*corev1.PersistentVolumeClaim)
			volumeID, tags, err := processPersistentVolumeClaim(pvc)
			if err != nil || len(tags) == 0 {
				return
			}
			ec2Client.tagVolume(volumeID, tags)
		},
		UpdateFunc: func(old, new interface{}) {
			if !provisionedByAwsEbs(new.(metav1.Object)) {
				return
			}

			newOne := new.(*corev1.PersistentVolumeClaim)
			oldOne := old.(*corev1.PersistentVolumeClaim)
			if newOne.ResourceVersion == oldOne.ResourceVersion {
				log.Debugln("ResourceVersion are the same")
				return
			}
			// TODO: Handle removed tags
			log.Infoln("Need to reconcile tags")
			volumeID, tags, err := processPersistentVolumeClaim(newOne)
			if err != nil || len(tags) == 0 {
				return
			}
			ec2Client.tagVolume(volumeID, tags)
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

	annotations := pvc.GetObjectMeta().GetAnnotations()
	// Skip if the annotation says to ignore this PVC
	if _, ok := annotations["aws-ebs-tagger/ignore"]; ok {
		return tags
	}

	// Set the default tags
	for k, v := range defaultTags {
		if strings.HasPrefix(k, "kubernetes.io") || k == "Name" {
			log.Errorln(k, "is a restricted tag. Skipping...")
			continue
		}
		tags[k] = v
	}

	tagString, ok := annotations["aws-ebs-tagger/tags"]
	if !ok {
		log.Debugln("Does not have aws-ebs-tagger/tags annotation")
		return tags
	}
	err := json.Unmarshal([]byte(tagString), &customTags)
	if err != nil {
		log.Errorln("Failed to Unmarshal JSON:", err)
	}

	for k, v := range customTags {
		if strings.HasPrefix(k, "kubernetes.io") || k == "Name" {
			log.Errorln(k, "is a restricted tag. Skipping...")
			continue
		}
		tags[k] = v
	}

	return tags
}

func provisionedByAwsEbs(mObj metav1.Object) bool {
	annotations := mObj.GetAnnotations()
	if provisionedBy, ok := annotations["volume.beta.kubernetes.io/storage-provisioner"]; !ok {
		log.Debugln("no volume.beta.kubernetes.io/storage-provisioner annotation")
		return false
	} else if provisionedBy == "kubernetes.io/aws-ebs" {
		return true
	}
	return false
}

func processPersistentVolumeClaim(pvc *corev1.PersistentVolumeClaim) (string, map[string]string, error) {
	tags := buildTags(pvc)
	log.Debugln(tags)

	pv, err := k8sClient.CoreV1().PersistentVolumes().Get(context.TODO(), pvc.Spec.VolumeName, metav1.GetOptions{})
	if err != nil {
		log.Errorf("Get PV from kubernetes cluster error:%v", err)
		return "", nil, err
	}

	volumeID := parseAWSVolumeID(pv.Spec.PersistentVolumeSource.AWSElasticBlockStore.VolumeID)
	log.Debugln("parsed volumeID:", volumeID)
	if len(volumeID) == 0 {
		log.Errorf("Get PV from kubernetes cluster error:%v", err)
		return "", nil, errors.New("Cannot parse VolumeID")
	}

	return volumeID, tags, nil
}
