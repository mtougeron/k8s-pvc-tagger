package main

import (
	"context"
	"fmt"
	"maps"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	log "github.com/sirupsen/logrus"
	"google.golang.org/api/compute/v1"
	"k8s.io/apimachinery/pkg/util/wait"
)

type GCPClient interface {
	GetDisk(project, zone, name string) (*compute.Disk, error)
	SetDiskLabels(project, zone, name string, labelReq *compute.ZoneSetLabelsRequest) (*compute.Operation, error)
	GetGCEOp(project, zone, name string) (*compute.Operation, error)
}

type gcpClient struct {
	gce *compute.Service
}

func newGCPClient(ctx context.Context) (GCPClient, error) {
	client, err := compute.NewService(ctx)
	if err != nil {
		return nil, err
	}
	return &gcpClient{gce: client}, nil
}

func (c *gcpClient) GetDisk(project, zone, name string) (*compute.Disk, error) {
	return c.gce.Disks.Get(project, zone, name).Do()
}

func (c *gcpClient) SetDiskLabels(project, zone, name string, labelReq *compute.ZoneSetLabelsRequest) (*compute.Operation, error) {
	return c.gce.Disks.SetLabels(project, zone, name, labelReq).Do()
}

func (c *gcpClient) GetGCEOp(project, zone, name string) (*compute.Operation, error) {
	return c.gce.ZoneOperations.Get(project, zone, name).Do()
}

func addPDVolumeLabels(c GCPClient, volumeID string, labels map[string]string, storageclass string) {
	sanitizedLabels := sanitizeLabelsForGCP(labels)
	log.Debugf("labels to add to PD volume: %s: %s", volumeID, sanitizedLabels)

	project, location, name, err := parseVolumeID(volumeID)
	if err != nil {
		log.Error(err)
		return
	}
	disk, err := c.GetDisk(project, location, name)
	if err != nil {
		log.Error(err)
		return
	}

	// merge existing disk labels with new labels:
	updatedLabels := make(map[string]string)
	if disk.Labels != nil {
		updatedLabels = maps.Clone(disk.Labels)
	}
	maps.Copy(updatedLabels, sanitizedLabels)
	if maps.Equal(disk.Labels, updatedLabels) {
		log.Debug("labels already set on PD")
		return
	}

	req := &compute.ZoneSetLabelsRequest{
		Labels:           updatedLabels,
		LabelFingerprint: disk.LabelFingerprint,
	}
	op, err := c.SetDiskLabels(project, location, name, req)
	if err != nil {
		log.Errorf("failed to set labels on PD: %s", err)
		promActionsTotal.With(prometheus.Labels{"status": "error", "storageclass": storageclass}).Inc()
		return
	}

	waitForCompletion := func(_ context.Context) (bool, error) {
		resp, err := c.GetGCEOp(project, location, op.Name)
		if err != nil {
			return false, fmt.Errorf("failed to set labels on PD %s: %s", disk.Name, err)
		}
		return resp.Status == "DONE", nil
	}
	if err := wait.PollUntilContextTimeout(context.TODO(),
		time.Second,
		time.Minute,
		false,
		waitForCompletion); err != nil {
		log.Errorf("set label operation failed: %s", err)
		return
	}

	log.Debug("successfully set labels on PD")
	promActionsTotal.With(prometheus.Labels{"status": "success", "storageclass": storageclass}).Inc()
}

func deletePDVolumeLabels(c GCPClient, volumeID string, keys []string, storageclass string) {
	if len(keys) == 0 {
		return
	}
	sanitizedKeys := sanitizeKeysForGCP(keys)
	log.Debugf("labels to delete from PD volume: %s: %s", volumeID, sanitizedKeys)

	project, location, name, err := parseVolumeID(volumeID)
	if err != nil {
		log.Error(err)
		return
	}
	disk, err := c.GetDisk(project, location, name)
	if err != nil {
		log.Error(err)
		return
	}
	// if disk.Labels is nil, then there are no labels to delete
	if disk.Labels == nil {
		return
	}

	updatedLabels := maps.Clone(disk.Labels)
	for _, k := range sanitizedKeys {
		delete(updatedLabels, k)
	}
	if maps.Equal(disk.Labels, updatedLabels) {
		return
	}

	req := &compute.ZoneSetLabelsRequest{
		Labels:           updatedLabels,
		LabelFingerprint: disk.LabelFingerprint,
	}
	op, err := c.SetDiskLabels(project, location, name, req)
	if err != nil {
		log.Errorf("failed to delete labels from PD: %s", err)
		promActionsTotal.With(prometheus.Labels{"status": "error", "storageclass": storageclass}).Inc()
		return
	}

	waitForCompletion := func(_ context.Context) (bool, error) {
		resp, err := c.GetGCEOp(project, location, op.Name)
		if err != nil {
			return false, fmt.Errorf("failed retrieve status of label update operation: %s", err)
		}
		return resp.Status == "DONE", nil
	}
	if err := wait.PollUntilContextTimeout(context.TODO(),
		time.Second,
		time.Minute,
		false,
		waitForCompletion); err != nil {
		promActionsTotal.With(prometheus.Labels{"status": "error", "storageclass": storageclass}).Inc()
		log.Errorf("delete label operation failed: %s", err)
		return
	}

	log.Debug("successfully deleted labels from PD")
	promActionsTotal.With(prometheus.Labels{"status": "success", "storageclass": storageclass}).Inc()
}

func parseVolumeID(id string) (string, string, string, error) {
	parts := strings.Split(id, "/")
	if len(parts) < 5 {
		return "", "", "", fmt.Errorf("invalid volume handle format")
	}
	project := parts[1]
	location := parts[3]
	name := parts[5]
	return project, location, name, nil
}

func sanitizeLabelsForGCP(labels map[string]string) map[string]string {
	newLabels := make(map[string]string, len(labels))
	for k, v := range labels {
		newLabels[sanitizeKeyForGCP(k)] = sanitizeValueForGCP(v)
	}
	return newLabels
}

func sanitizeKeysForGCP(keys []string) []string {
	newKeys := make([]string, len(keys))
	for i, k := range keys {
		newKeys[i] = sanitizeKeyForGCP(k)
	}
	return newKeys
}

// sanitizeKeyForGCP sanitizes a Kubernetes label key to fit GCP's label key constraints
func sanitizeKeyForGCP(key string) string {
	key = strings.ToLower(key)
	key = strings.NewReplacer("/", "_", ".", "-").Replace(key) // Replace disallowed characters
	key = strings.TrimRight(key, "-_")                         // Ensure it does not end with '-' or '_'

	if len(key) > 63 {
		key = key[:63]
	}
	return key
}

// sanitizeKeyForGCP sanitizes a Kubernetes label value to fit GCP's label value constraints
func sanitizeValueForGCP(value string) string {
	if len(value) > 63 {
		value = value[:63]
	}
	return value
}
