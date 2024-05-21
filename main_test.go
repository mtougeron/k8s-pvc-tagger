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
)

func Test_parseCsv(t *testing.T) {
	tests := []struct {
		name string
		csv  string
		want map[string]string
	}{
		{
			name: "empty string",
			csv:  "",
			want: map[string]string{},
		},
		{
			name: "single key/value string",
			csv:  "touge=me",
			want: map[string]string{"touge": "me"},
		},
		{
			name: "multiple key/value string",
			csv:  "touge=me,foo=bar",
			want: map[string]string{"touge": "me", "foo": "bar"},
		},
		{
			name: "invalid string",
			csv:  "foo",
			want: map[string]string{},
		},
		{
			name: "invalid key string",
			csv:  "=foo",
			want: map[string]string{},
		},
		{
			name: "invalid value string",
			csv:  "foo=",
			want: map[string]string{},
		},
		{
			name: "double delim",
			csv:  "foo=bar,,touge=me",
			want: map[string]string{"touge": "me", "foo": "bar"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseCsv(tt.csv); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("parseCsv() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_parseCopyLabels(t *testing.T) {
	tests := []struct {
		name             string
		copyLabelsString string
		want             []string
	}{
		{
			name:             "copy all labels",
			copyLabelsString: "*",
			want:             []string{"*"},
		},
		{
			name:             "copy one label",
			copyLabelsString: "foo",
			want:             []string{"foo"},
		},
		{
			name:             "copy multiple labels",
			copyLabelsString: "foo,bar",
			want:             []string{"foo", "bar"},
		},
		{
			name:             "empty values in list",
			copyLabelsString: "foo,bar",
			want:             []string{"foo", "bar"},
		},
		{
			name:             "copy no labels",
			copyLabelsString: "",
			want:             []string{},
		},
<<<<<<< HEAD
		{
			name:             "empty values in list",
			copyLabelsString: "foo,,bar",
			want:             []string{"foo", "bar"},
		},
||||||| parent of 36790c1 (handle empty strings in copy-labels list)
=======
		{
			name:             "empty values in list are removed",
			copyLabelsString: "foo,,bar",
			want:             []string{"foo", "bar"},
		},
>>>>>>> 36790c1 (handle empty strings in copy-labels list)
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseCopyLabels(tt.copyLabelsString); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("parseCopyLabels() = %v, want %v", got, tt.want)
			}
		})
	}
}
