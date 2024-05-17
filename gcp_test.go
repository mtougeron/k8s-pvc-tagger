package main

import (
	"maps"
	"reflect"
	"strings"
	"testing"

	"google.golang.org/api/compute/v1"
)

type fakeGCPClient struct {
	fakeGetDisk       func(project, zone, name string) (*compute.Disk, error)
	fakeSetDiskLabels func(project, zone, name string, labelReq *compute.ZoneSetLabelsRequest) (*compute.Operation, error)
	fakeGetGCEOp      func(project, zone, name string) (*compute.Operation, error)

	setLabelsCalled bool
}

func (c *fakeGCPClient) GetDisk(project, zone, name string) (*compute.Disk, error) {
	if c.fakeGetDisk == nil {
		return nil, nil
	}
	return c.fakeGetDisk(project, zone, name)
}

func (c *fakeGCPClient) SetDiskLabels(project, zone, name string, labelReq *compute.ZoneSetLabelsRequest) (*compute.Operation, error) {
	c.setLabelsCalled = true
	if c.fakeSetDiskLabels == nil {
		return nil, nil
	}
	return c.fakeSetDiskLabels(project, zone, name, labelReq)
}

func (c *fakeGCPClient) GetGCEOp(project, zone, name string) (*compute.Operation, error) {
	if c.fakeSetDiskLabels == nil {
		return nil, nil
	}
	return c.fakeGetGCEOp(project, zone, name)
}

func setupFakeGCPClient(t *testing.T, currentLabels map[string]string, expectedSetLabels map[string]string) *fakeGCPClient {
	return &fakeGCPClient{
		fakeGetDisk: func(project, zone, name string) (*compute.Disk, error) {
			return &compute.Disk{Labels: currentLabels}, nil
		},
		fakeSetDiskLabels: func(project, zone, name string, labelReq *compute.ZoneSetLabelsRequest) (*compute.Operation, error) {
			if !maps.Equal(labelReq.Labels, expectedSetLabels) {
				t.Errorf("SetDiskLabels(), got labels = %v, want = %v", labelReq.Labels, expectedSetLabels)
			}
			return &compute.Operation{Status: "PENDING"}, nil
		},
		fakeGetGCEOp: func(project, zone, name string) (*compute.Operation, error) {
			return &compute.Operation{Status: "DONE"}, nil
		},
	}
}

func TestAddPDVolumeLabels(t *testing.T) {
	tests := []struct {
		name                  string
		volumeID              string
		currentLabels         map[string]string
		newPvcLabels          map[string]string
		expectSetLabelsCalled bool
		expectedSetLabels     map[string]string
	}{
		{
			name:                  "add new labels",
			volumeID:              "projects/myproject/zones/myzone/disks/mydisk",
			currentLabels:         map[string]string{"key1": "val1", "key2": "val2"},
			newPvcLabels:          map[string]string{"foo": "bar", "dom.tld/key": "value"},
			expectSetLabelsCalled: true,
			expectedSetLabels:     map[string]string{"key1": "val1", "key2": "val2", "foo": "bar", "dom-tld_key": "value"},
		},
		{
			name:                  "labels already set",
			volumeID:              "projects/myproject/zones/myzone/disks/mydisk",
			currentLabels:         map[string]string{"key1": "val1", "key2": "val2"},
			expectSetLabelsCalled: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := setupFakeGCPClient(t, tt.currentLabels, tt.expectedSetLabels)

			addPDVolumeLabels(client, tt.volumeID, tt.newPvcLabels, "storage-ssd")

			if client.setLabelsCalled != tt.expectSetLabelsCalled {
				t.Error("SetDiskLabels() was not called")
			}
		})
	}
}

func TestDeletePDVolumeLabels(t *testing.T) {
	tests := []struct {
		name                  string
		volumeID              string
		currentLabels         map[string]string
		labelsToDelete        []string
		expectSetLabelsCalled bool
		expectedSetLabels     map[string]string
	}{
		{
			name:                  "delete existing labels",
			volumeID:              "projects/myproject/zones/myzone/disks/mydisk",
			currentLabels:         map[string]string{"key1": "val1", "key2": "val2", "dom-tld_key": "bar"},
			labelsToDelete:        []string{"key1", "dom.tld/key"},
			expectSetLabelsCalled: true,
			expectedSetLabels:     map[string]string{"key2": "val2"},
		},
		{
			name:                  "no labels to delete",
			volumeID:              "projects/myproject/zones/myzone/disks/mydisk",
			currentLabels:         map[string]string{"key1": "val1", "key2": "val2"},
			labelsToDelete:        []string{},
			expectSetLabelsCalled: false,
		},
		{
			name:                  "no matching labels to delete",
			volumeID:              "projects/myproject/zones/myzone/disks/mydisk",
			currentLabels:         map[string]string{"key1": "val1", "key2": "val2"},
			labelsToDelete:        []string{"foo"},
			expectSetLabelsCalled: false,
		},
		{
			name:                  "all labels deleted",
			volumeID:              "projects/myproject/zones/myzone/disks/mydisk",
			currentLabels:         map[string]string{"key1": "val1"},
			labelsToDelete:        []string{"key1"},
			expectSetLabelsCalled: true,
			expectedSetLabels:     map[string]string{},
		},
		{
			name:                  "no labels on disk",
			volumeID:              "projects/myproject/zones/myzone/disks/mydisk",
			currentLabels:         nil,
			labelsToDelete:        []string{"foo"},
			expectSetLabelsCalled: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := setupFakeGCPClient(t, tt.currentLabels, tt.expectedSetLabels)

			deletePDVolumeLabels(client, tt.volumeID, tt.labelsToDelete, "storage-ssd")

			if client.setLabelsCalled != tt.expectSetLabelsCalled {
				t.Error("SetDiskLabels() was not called")
			}
		})
	}
}

func TestSanitizeLabelsForGCP(t *testing.T) {
	tests := []struct {
		name   string
		labels map[string]string
		want   map[string]string
	}{
		{
			name: "simple labels",
			labels: map[string]string{
				"Example/Key": "Example Value",
				"Another.Key": "Another Value",
			},
			want: map[string]string{
				"example_key": "Example Value",
				"another-key": "Another Value",
			},
		},
		{
			name: "labels with special characters",
			labels: map[string]string{
				"Domain.com/Key":  "Value_1",
				"Project.Version": "Version-1.2.3",
			},
			want: map[string]string{
				"domain-com_key":  "Value_1",
				"project-version": "Version-1.2.3",
			},
		},
		{
			name: "labels exceeding maximum length",
			labels: map[string]string{
				strings.Repeat("a", 70): strings.Repeat("b", 70),
			},
			want: map[string]string{
				strings.Repeat("a", 63): strings.Repeat("b", 63),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sanitizeLabelsForGCP(tt.labels); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("sanitizeLabelsForGCP(), got = %v, want = %v", got, tt.want)
			}
		})
	}
}

func TestParseVolumeID(t *testing.T) {
	tests := []struct {
		name         string
		id           string
		wantProject  string
		wantLocation string
		wantName     string
		wantErr      bool
	}{
		{
			name:         "valid volume ID",
			id:           "projects/my-project/zones/us-central1/disks/my-disk",
			wantProject:  "my-project",
			wantLocation: "us-central1",
			wantName:     "my-disk",
			wantErr:      false,
		},
		{
			name:         "missing parts",
			id:           "projects/my-project/zones/",
			wantProject:  "",
			wantLocation: "",
			wantName:     "",
			wantErr:      true,
		},
		{
			name:         "empty input",
			id:           "",
			wantProject:  "",
			wantLocation: "",
			wantName:     "",
			wantErr:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			project, location, name, err := parseVolumeID(tt.id)
			if (err != nil) != tt.wantErr {
				t.Errorf("parseVolumeID() error = %v, wantErr %v", err, tt.wantErr)
			}
			if project != tt.wantProject {
				t.Errorf("Expected project %q, got %q", tt.wantProject, project)
			}
			if location != tt.wantLocation {
				t.Errorf("Expected location %q, got %q", tt.wantLocation, location)
			}
			if name != tt.wantName {
				t.Errorf("Expected name %q, got %q", tt.wantName, name)
			}
		})
	}
}
