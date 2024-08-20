package main

import (
	"context"
	"errors"
	"fmt"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/arm"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/runtime"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/compute/armcompute"
	"github.com/prometheus/client_golang/prometheus"
	log "github.com/sirupsen/logrus"
	"maps"
	"strings"
	"time"
)

var (
	ErrAzureTooManyTags error = errors.New("Only up to 50 tags can be set on an azure resource")
	ErrAzureValueToLong error = errors.New("A value can only contain 256 characters")
)

type DiskTags = map[string]*string
type AzureSubscription = string

type AzureClient interface {
	GetDiskTags(ctx context.Context, subscription AzureSubscription, resourceGroupName string, diskName string) (DiskTags, error)
	SetDiskTags(ctx context.Context, subscription AzureSubscription, resourceGroupName string, diskName string, tags DiskTags) error
}

type azureClient struct {
	credentials azcore.TokenCredential
	clients     map[AzureSubscription]*armcompute.DisksClient
}

func NewAzureClient() (AzureClient, error) {
	creds, err := azidentity.NewDefaultAzureCredential(nil)
	if err != nil {
		return nil, err
	}

	return azureClient{creds, map[AzureSubscription]*armcompute.DisksClient{}}, err
}

func (self azureClient) getClient(subscriptionID AzureSubscription) (*armcompute.DisksClient, error) {
	if client, ok := self.clients[subscriptionID]; ok {
		return client, nil
	}

	client, err := armcompute.NewDisksClient(subscriptionID, self.credentials, &arm.ClientOptions{})
	if err != nil {
		return nil, err
	}

	self.clients[subscriptionID] = client
	return client, nil
}

func (self azureClient) GetDiskTags(ctx context.Context, subscription AzureSubscription, resourceGroupName string, diskName string) (DiskTags, error) {
	client, err := self.getClient(subscription)
	if err != nil {
		return nil, err
	}

	disk, err := client.Get(ctx, resourceGroupName, diskName, &armcompute.DisksClientGetOptions{})
	if err != nil {
		return nil, fmt.Errorf("could not get the tags for: %w", err)
	}
	return disk.Tags, nil
}

func (self azureClient) SetDiskTags(ctx context.Context, subscription AzureSubscription, resourceGroupName string, diskName string, tags DiskTags) error {
	client, err := self.getClient(subscription)
	if err != nil {
		return err
	}

	poller, err := client.BeginUpdate(ctx, resourceGroupName, diskName, armcompute.DiskUpdate{Tags: tags}, &armcompute.DisksClientBeginUpdateOptions{})
	if err != nil {
		return fmt.Errorf("could not set the tags for: %w", err)
	}

	_, err = poller.PollUntilDone(ctx, &runtime.PollUntilDoneOptions{5 * time.Second})
	if err != nil {
		return fmt.Errorf("could not set the tags for failed at polling: %w", err)
	}
	return nil
}

func parseAzureVolumeID(volumeID string) (subscription string, resourceGroup string, diskName string, err error) {
	// '/subscriptions/{subscription}/resourceGroups/{resourceGroup}/providers/Microsoft.Compute/disks/{diskname}"'
	fields := strings.Split(volumeID, "/")
	if len(fields) != 9 {
		return "", "", "", errors.New("invalid volume id")
	}
	subscription = fields[2]
	resourceGroup = fields[4]
	diskName = fields[8]
	return subscription, resourceGroup, diskName, nil
}

func sanitizeLabelsForAzure(tags map[string]string) (DiskTags, error) {
	diskTags := make(DiskTags)
	if len(tags) > 50 {
		return nil, ErrAzureTooManyTags
	}
	for k, v := range tags {
		k = sanitizeKeyForAzure(k)
		value, err := sanitizeValueForAzure(v)
		if err != nil {
			return nil, err
		}

		diskTags[k] = &value
	}

	return diskTags, nil
}

func sanitizeKeyForAzure(s string) string {
	// remove forbidden characters
	if strings.ContainsAny(s, `<>%&\?/`) {
		for _, c := range `<>%&\?/` {
			s = strings.ReplaceAll(s, string(c), "")
		}
	}

	// truncate the key the max length for azure
	if len(s) > 512 {
		s = s[:512]
	}

	return s
}

func sanitizeValueForAzure(s string) (string, error) {
	// the value can contain at most 256 characters
	if len(s) > 256 {
		return "", fmt.Errorf("%s value is invalid: %w", s, ErrAzureValueToLong)
	}
	return s, nil
}

func UpdateAzurePodLabels(ctx context.Context, client AzureClient, volumeID string, tags map[string]string, removedTags []string, storageclass string) error {
	sanitizedLabels, err := sanitizeLabelsForAzure(tags)
	if err != nil {
		return err
	}

	log.Debugf("labels to add to PD volume: %s: %v", volumeID, sanitizedLabels)
	subscription, resourceGroup, diskName, err := parseAzureVolumeID(volumeID)
	if err != nil {
		return err
	}

	existingTags, err := client.GetDiskTags(ctx, subscription, resourceGroup, diskName)
	if err != nil {
		return err
	}

	// merge existing disk labels with new labels:
	updatedTags := make(DiskTags)
	if existingTags != nil {
		updatedTags = maps.Clone(existingTags)
	}
	maps.Copy(updatedTags, sanitizedLabels)

	for _, tag := range removedTags {
		delete(updatedTags, tag)
	}

	if maps.Equal(existingTags, updatedTags) {
		log.Debug("labels already set on PD")
		return nil
	}

	err = client.SetDiskTags(ctx, subscription, resourceGroup, diskName, updatedTags)
	if err != nil {
		promActionsTotal.With(prometheus.Labels{"status": "error", "storageclass": storageclass}).Inc()
		return err
	}

	log.Debug("successfully set labels on PD")
	promActionsTotal.With(prometheus.Labels{"status": "success", "storageclass": storageclass}).Inc()
	return nil
}
