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
