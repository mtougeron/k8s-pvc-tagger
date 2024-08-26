package main

import (
	"fmt"
	"github.com/stretchr/testify/assert"
	"strings"
	"testing"
)

func Test_parseAzureVolumeID(t *testing.T) {
	type args struct {
		volumeID string
	}
	tests := []struct {
		name              string
		args              args
		wantSubscription  string
		wantResourceGroup string
		wantDiskName      string
		wantErr           bool
	}{
		{
			name:              "test using a correct volume ID",
			args:              args{volumeID: "/subscriptions/{subscription}/resourceGroups/{resourceGroup}/providers/Microsoft.Compute/disks/{diskname}"},
			wantSubscription:  "{subscription}",
			wantResourceGroup: "{resourceGroup}",
			wantDiskName:      "{diskname}",
			wantErr:           false,
		},
		{
			name:              "test using a correct volume ID",
			args:              args{volumeID: "/subscriptions/{subscription}/resourceGroups/{resourceGroup}/providers/Microsoft.Compute/disks"},
			wantSubscription:  "",
			wantResourceGroup: "",
			wantDiskName:      "",
			wantErr:           true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			gotSubscription, gotResourceGroup, gotDiskName, err := parseAzureVolumeID(tt.args.volumeID)
			if (err != nil) != tt.wantErr {
				t.Errorf("parseAzureVolumeID() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if gotSubscription != tt.wantSubscription {
				t.Errorf("parseAzureVolumeID() gotSubscription = %v, want %v", gotSubscription, tt.wantSubscription)
			}
			if gotResourceGroup != tt.wantResourceGroup {
				t.Errorf("parseAzureVolumeID() gotResourceGroup = %v, want %v", gotResourceGroup, tt.wantResourceGroup)
			}
			if gotDiskName != tt.wantDiskName {
				t.Errorf("parseAzureVolumeID() gotDiskName = %v, want %v", gotDiskName, tt.wantDiskName)
			}
		})
	}
}

func Test_sanitizeKeyForAzure(t *testing.T) {
	type args struct {
		s string
	}
	var tests = []struct {
		name string
		args args
		want string
	}{
		{
			name: "the key should be trimmed to 512 characters",
			args: args{
				s: strings.Repeat("1", 513),
			},
			want: strings.Repeat("1", 512),
		},
		{
			name: "the key should remove all invalid characters",
			args: args{
				s: `1<>&\?%/`,
			},
			want: "1",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := sanitizeKeyForAzure(tt.args.s); got != tt.want {
				t.Errorf("sanitizeKeyForAzure() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_sanitizeValueForAzure(t *testing.T) {
	type args struct {
		s string
	}
	tests := []struct {
		name    string
		args    args
		want    string
		wantErr bool
	}{
		{
			name:    "a valid value",
			args:    args{strings.Repeat("1", 256)},
			want:    strings.Repeat("1", 256),
			wantErr: false,
		},

		{
			name:    "the max value lenght is 256 characters",
			args:    args{strings.Repeat("1", 257)},
			want:    "",
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := sanitizeValueForAzure(tt.args.s)
			if (err != nil) != tt.wantErr {
				t.Errorf("sanitizeValueForAzure() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("sanitizeValueForAzure() got = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_sanitizeLabelsForAzure(t *testing.T) {
	t.Run("azure supports up to 50 labels", func(t *testing.T) {
		t.Parallel()
		tags := map[string]string{}
		for x := 0; x < 50; x++ {
			v := fmt.Sprintf("%d", x)
			tags[v] = v
		}
		_, err := sanitizeLabelsForAzure(tags)
		assert.NoError(t, err)

		tags["51"] = "51"
		_, err = sanitizeLabelsForAzure(tags)
		assert.ErrorIs(t, err, ErrAzureTooManyTags)
	})
}

func Test_diskScope(t *testing.T) {
	type args struct {
		subscription      string
		resourceGroupName string
		diskName          string
	}
	tests := []struct {
		name string
		args args
		want string
	}{
		{
			"it should generate a valid scope for disks",
			args{
				subscription:      "sub",
				resourceGroupName: "resource-name",
				diskName:          "disk-name",
			},
			"subscriptions/sub/resourceGroups/resource-name/providers/Microsoft.Compute/disks/disk-name",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equalf(t, tt.want, diskScope(tt.args.subscription, tt.args.resourceGroupName, tt.args.diskName), "diskScope(%v, %v, %v)", tt.args.subscription, tt.args.resourceGroupName, tt.args.diskName)
		})
	}
}
