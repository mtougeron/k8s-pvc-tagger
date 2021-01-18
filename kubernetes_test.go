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
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func Test_parseAWSVolumeID(t *testing.T) {
	tests := []struct {
		name        string
		k8sVolumeID string
		want        string
	}{
		{
			name:        "full AWSElasticBlockStore.VolumeID",
			k8sVolumeID: "aws://us-east-1d/vol-089747b9fac6ab469",
			want:        "vol-089747b9fac6ab469",
		},
		{
			name:        "invalid AWSElasticBlockStore.VolumeID",
			k8sVolumeID: "aws://something-else/vol-089747b9fac6ab469",
			want:        "",
		},
		{
			name:        "partial AWSElasticBlockStore.VolumeID",
			k8sVolumeID: "vol-abc123",
			want:        "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseAWSVolumeID(tt.k8sVolumeID); got != tt.want {
				t.Errorf("parseAWSVolumeID() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_isValidTagName(t *testing.T) {
	tests := []struct {
		name string
		key  string
		want bool
	}{
		{
			name: "invalid prefix",
			key:  "kubernetes.io/something",
			want: false,
		},
		{
			name: "valid prefix",
			key:  "my-name.io/something",
			want: true,
		},
		{
			name: "invalid Name",
			key:  "Name",
			want: false,
		},
		{
			name: "invalid KubernetesCluster",
			key:  "KubernetesCluster",
			want: false,
		},
		{
			name: "valid annotation",
			key:  "something",
			want: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isValidTagName(tt.key); got != tt.want {
				t.Errorf("isValidTagName() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_provisionedByAwsEbs(t *testing.T) {

	pvc := &corev1.PersistentVolumeClaim{}
	pvc.SetName("my-pvc")

	tests := []struct {
		name        string
		annotations map[string]string
		want        bool
	}{
		{
			name:        "valid provisioner in-tree aws-ebs",
			annotations: map[string]string{"volume.beta.kubernetes.io/storage-provisioner": "kubernetes.io/aws-ebs"},
			want:        true,
		},
		{
			name:        "valid provisioner ebs.csi.aws.com",
			annotations: map[string]string{"volume.beta.kubernetes.io/storage-provisioner": "ebs.csi.aws.com"},
			want:        true,
		},
		{
			name:        "invalid provisioner",
			annotations: map[string]string{"volume.beta.kubernetes.io/storage-provisioner": "something else"},
			want:        false,
		},
		{
			name:        "provisioner not set",
			annotations: map[string]string{"some annotation": "something else"},
			want:        false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pvc.SetAnnotations(tt.annotations)
			if got := provisionedByAwsEbs(pvc); got != tt.want {
				t.Errorf("provisionedByAwsEbs() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_buildTags(t *testing.T) {

	pvc := &corev1.PersistentVolumeClaim{}
	pvc.SetName("my-pvc")

	tests := []struct {
		name        string
		defaultTags map[string]string
		annotations map[string]string
		want        map[string]string
	}{
		{
			name:        "ignore annotation set",
			defaultTags: map[string]string{},
			annotations: map[string]string{"aws-ebs-tagger/ignore": ""},
			want:        map[string]string{},
		},
		{
			name:        "ignore annotation set with default tags",
			defaultTags: map[string]string{"foo": "bar"},
			annotations: map[string]string{"aws-ebs-tagger/ignore": ""},
			want:        map[string]string{},
		},
		{
			name:        "ignore annotation set with tags annotation set",
			defaultTags: map[string]string{},
			annotations: map[string]string{"aws-ebs-tagger/ignore": "exists", "aws-ebs-tagger/tags": "{\"foo\": \"bar\"}"},
			want:        map[string]string{},
		},
		{
			name:        "tags annotation not set with default tags",
			defaultTags: map[string]string{"foo": "bar", "something": "else"},
			annotations: map[string]string{},
			want:        map[string]string{"foo": "bar", "something": "else"},
		},
		{
			name:        "tags annotation not set with no default tags",
			defaultTags: map[string]string{},
			annotations: map[string]string{},
			want:        map[string]string{},
		},
		{
			name:        "tags annotation set empty with no default tags",
			defaultTags: map[string]string{},
			annotations: map[string]string{"aws-ebs-tagger/tags": ""},
			want:        map[string]string{},
		},
		{
			name:        "tags annotation set with no default tags",
			defaultTags: map[string]string{},
			annotations: map[string]string{"aws-ebs-tagger/tags": "{\"foo\": \"bar\"}"},
			want:        map[string]string{"foo": "bar"},
		},
		{
			name:        "tags annotation set with default tags",
			defaultTags: map[string]string{"foo": "bar"},
			annotations: map[string]string{"aws-ebs-tagger/tags": "{\"something\": \"else\"}"},
			want:        map[string]string{"foo": "bar", "something": "else"},
		},
		{
			name:        "tags annotation set with default tags with override",
			defaultTags: map[string]string{"foo": "foo"},
			annotations: map[string]string{"aws-ebs-tagger/tags": "{\"foo\": \"bar\", \"something\": \"else\"}"},
			want:        map[string]string{"foo": "bar", "something": "else"},
		},
		{
			name:        "tags annotation invalid json with no default tags",
			defaultTags: map[string]string{},
			annotations: map[string]string{"aws-ebs-tagger/tags": "'asdas:\"asdasd\""},
			want:        map[string]string{},
		},
		{
			name:        "tags annotation invalid json with default tags",
			defaultTags: map[string]string{"foo": "bar"},
			annotations: map[string]string{"aws-ebs-tagger/tags": "'asdas:\"asdasd\""},
			want:        map[string]string{"foo": "bar"},
		},
		{
			name:        "tags annotation set with invalid name with no default tags",
			defaultTags: map[string]string{},
			annotations: map[string]string{"aws-ebs-tagger/tags": "{\"foo\": \"bar\", \"kubernetes.io/foo\": \"bar\"}"},
			want:        map[string]string{"foo": "bar"},
		},
		{
			name:        "tags annotation set with invalid default tags",
			defaultTags: map[string]string{"kubernetes.io/foo": "bar"},
			annotations: map[string]string{"aws-ebs-tagger/tags": "{\"something\": \"else\"}"},
			want:        map[string]string{"something": "else"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pvc.SetAnnotations(tt.annotations)
			defaultTags = tt.defaultTags
			if got := buildTags(pvc); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("buildTags() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_annotationPrefix(t *testing.T) {

	pvc := &corev1.PersistentVolumeClaim{}
	pvc.SetName("my-pvc")

	tests := []struct {
		name             string
		annotationPrefix string
		defaultTags      map[string]string
		annotations      map[string]string
		want             map[string]string
	}{
		{
			name:             "annotationPrefix with proper ignore",
			annotationPrefix: "something-else",
			defaultTags:      map[string]string{"foo": "bar"},
			annotations:      map[string]string{"something-else/ignore": ""},
			want:             map[string]string{},
		},
		{
			name:             "annotationPrefix with different ignore",
			annotationPrefix: "something-else",
			defaultTags:      map[string]string{"foo": "bar"},
			annotations:      map[string]string{"aws-ebs-tagger/ignore": ""},
			want:             map[string]string{"foo": "bar"},
		},
		{
			name:             "annotationPrefix with default and custom tags",
			annotationPrefix: "something-else",
			defaultTags:      map[string]string{"foo": "bar"},
			annotations:      map[string]string{"something-else/tags": "{\"something\": \"else\"}"},
			want:             map[string]string{"foo": "bar", "something": "else"},
		},
		{
			name:             "annotationPrefix with default and different custom tags",
			annotationPrefix: "something-else",
			defaultTags:      map[string]string{"foo": "bar"},
			annotations:      map[string]string{"aws-ebs-tagger/tags": "{\"something\": \"else\"}"},
			want:             map[string]string{"foo": "bar"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pvc.SetAnnotations(tt.annotations)
			annotationPrefix = tt.annotationPrefix
			defaultTags = tt.defaultTags
			if got := buildTags(pvc); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("buildTags() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_processPersistentVolumeClaim(t *testing.T) {
	volumeName := "pvc-1234"
	pvc := &corev1.PersistentVolumeClaim{}
	pvc.SetName("my-pvc")
	pvc.Spec.VolumeName = volumeName

	tests := []struct {
		name           string
		provisionedBy  string
		tagsAnnotation string
		volumeID       string
		wantedTags     map[string]string
		wantedVolumeID string
		wantedErr      bool
	}{
		{
			name:           "csi with valid tags and volume id",
			provisionedBy:  "ebs.csi.aws.com",
			tagsAnnotation: "{\"foo\": \"bar\"}",
			wantedTags:     map[string]string{"foo": "bar"},
			wantedVolumeID: "vol-12345",
			wantedErr:      false,
		},
		{
			name:           "in-tree with valid tags and volume id",
			provisionedBy:  "kubernetes.io/aws-ebs",
			tagsAnnotation: "{\"foo\": \"bar\"}",
			volumeID:       "aws://us-east-1a/vol-12345",
			wantedTags:     map[string]string{"foo": "bar"},
			wantedVolumeID: "vol-12345",
			wantedErr:      false,
		},
		{
			name:           "in-tree with valid tags and invalid volume id",
			provisionedBy:  "kubernetes.io/aws-ebs",
			tagsAnnotation: "{\"foo\": \"bar\"}",
			volumeID:       "asdf://us-east-1a/vol-12345",
			wantedTags:     nil,
			wantedVolumeID: "",
			wantedErr:      true,
		},
		{
			name:           "other with valid tags and volume id",
			provisionedBy:  "foo",
			tagsAnnotation: "{\"foo\": \"bar\"}",
			wantedTags:     nil,
			wantedVolumeID: "",
			wantedErr:      true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pvc.SetAnnotations(map[string]string{
				annotationPrefix + "/tags":                      tt.tagsAnnotation,
				"volume.beta.kubernetes.io/storage-provisioner": tt.provisionedBy,
			})

			var pvSpec corev1.PersistentVolumeSpec
			if tt.provisionedBy == "ebs.csi.aws.com" {
				pvSpec = corev1.PersistentVolumeSpec{
					PersistentVolumeSource: corev1.PersistentVolumeSource{
						CSI: &corev1.CSIPersistentVolumeSource{
							VolumeHandle: tt.wantedVolumeID,
						},
					},
				}
			} else {
				pvSpec = corev1.PersistentVolumeSpec{
					PersistentVolumeSource: corev1.PersistentVolumeSource{
						AWSElasticBlockStore: &corev1.AWSElasticBlockStoreVolumeSource{
							VolumeID: tt.volumeID,
						},
					},
				}
			}
			pv := &corev1.PersistentVolume{
				ObjectMeta: metav1.ObjectMeta{
					Name:        volumeName,
					Annotations: map[string]string{},
				},
				Spec: pvSpec,
			}
			k8sClient = fake.NewSimpleClientset(pv)
			volumeID, tags, err := processPersistentVolumeClaim(pvc)
			if (err == nil) == tt.wantedErr {
				t.Errorf("processPersistentVolumeClaim() err = %v, wantedErr %v", err, tt.wantedErr)
			}
			if volumeID != tt.wantedVolumeID {
				t.Errorf("processPersistentVolumeClaim() volumeID = %v, want %v", volumeID, tt.wantedVolumeID)
			}
			if !reflect.DeepEqual(tags, tt.wantedTags) {
				t.Errorf("processPersistentVolumeClaim() tags = %v, want %v", tags, tt.wantedTags)
			}
		})
	}

}
