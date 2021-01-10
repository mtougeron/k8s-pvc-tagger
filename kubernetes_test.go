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
	"testing"

	corev1 "k8s.io/api/core/v1"
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
			name:        "valid provisioner",
			annotations: map[string]string{"volume.beta.kubernetes.io/storage-provisioner": "kubernetes.io/aws-ebs"},
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