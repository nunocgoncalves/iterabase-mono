package main

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/digitalocean/godo"
)

const longestForgeWorkflowTimeout = 155 * time.Minute

type fakeDropletClient struct {
	droplets      []godo.Droplet
	deleteErrors  map[int]error
	requestedTags []string
	deletedIDs    []int
}

func (client *fakeDropletClient) ListByTag(_ context.Context, tag string, _ *godo.ListOptions) ([]godo.Droplet, *godo.Response, error) {
	client.requestedTags = append(client.requestedTags, tag)
	return client.droplets, &godo.Response{}, nil
}

func (client *fakeDropletClient) Delete(_ context.Context, id int) (*godo.Response, error) {
	client.deletedIDs = append(client.deletedIDs, id)
	return &godo.Response{}, client.deleteErrors[id]
}

type fakeVolumeClient struct {
	volumes      []godo.Volume
	deleteErrors map[string]error
	deletedIDs   []string
}

func (client *fakeVolumeClient) ListVolumes(_ context.Context, _ *godo.ListVolumeParams) ([]godo.Volume, *godo.Response, error) {
	return client.volumes, &godo.Response{}, nil
}

func (client *fakeVolumeClient) DeleteVolume(_ context.Context, id string) (*godo.Response, error) {
	client.deletedIDs = append(client.deletedIDs, id)
	return &godo.Response{}, client.deleteErrors[id]
}

func TestDefaultMaxAgeOutlivesLongestForgeWorkflow(t *testing.T) {
	t.Parallel()
	if defaultMaxAge <= longestForgeWorkflowTimeout {
		t.Fatalf("default reaper age %s must exceed longest Forge workflow bound %s", defaultMaxAge, longestForgeWorkflowTimeout)
	}
}

func TestReapDeletesOnlyExpiredTaggedOrphans(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 17, 16, 0, 0, 0, time.UTC)
	client := &fakeDropletClient{droplets: []godo.Droplet{
		{ID: 101, Name: "failed-cpu-run", Created: now.Add(-defaultMaxAge - time.Minute).Format(time.RFC3339), Tags: []string{"forge-e2e"}},
		{ID: 202, Name: "active-gpu-run", Created: now.Add(-longestForgeWorkflowTimeout).Format(time.RFC3339), Tags: []string{"forge-e2e"}},
	}}
	var output bytes.Buffer

	if err := reap(context.Background(), client, now, defaultMaxAge, &output); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(client.requestedTags, []string{"forge-e2e"}) {
		t.Fatalf("reaper tag queries = %v", client.requestedTags)
	}
	if !slices.Equal(client.deletedIDs, []int{101}) {
		t.Fatalf("deleted droplets = %v", client.deletedIDs)
	}
	if !strings.Contains(output.String(), "failed-cpu-run") {
		t.Fatalf("reaper evidence does not identify the orphan:\n%s", output.String())
	}
}

func TestReapVolumesDeletesOnlyExpiredTaggedStorage(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 17, 16, 0, 0, 0, time.UTC)
	client := &fakeVolumeClient{volumes: []godo.Volume{
		{ID: "expired", Name: "failed-workspace-ssd", CreatedAt: now.Add(-defaultMaxAge - time.Minute), Tags: []string{"forge-e2e"}},
		{ID: "active", Name: "active-workspace-ssd", CreatedAt: now.Add(-longestForgeWorkflowTimeout), Tags: []string{"forge-e2e"}},
		{ID: "foreign", Name: "customer-volume", CreatedAt: now.Add(-24 * time.Hour)},
	}}
	var output bytes.Buffer

	if err := reapVolumes(context.Background(), client, now, defaultMaxAge, &output); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(client.deletedIDs, []string{"expired"}) {
		t.Fatalf("deleted block volumes = %v", client.deletedIDs)
	}
	if !strings.Contains(output.String(), "failed-workspace-ssd") {
		t.Fatalf("volume reaper evidence does not identify the orphan:\n%s", output.String())
	}
}

func TestReapVolumesPropagatesAttachedVolumeRefusal(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 17, 16, 0, 0, 0, time.UTC)
	deleteErr := errors.New("volume is still attached")
	client := &fakeVolumeClient{
		volumes:      []godo.Volume{{ID: "attached", Name: "orphan", CreatedAt: now.Add(-4 * time.Hour), Tags: []string{"forge-e2e"}}},
		deleteErrors: map[string]error{"attached": deleteErr},
	}
	err := reapVolumes(context.Background(), client, now, defaultMaxAge, &bytes.Buffer{})
	if !errors.Is(err, deleteErr) {
		t.Fatalf("volume reaper error = %v, want %v", err, deleteErr)
	}
}

func TestReapContinuesAfterDeletionFailureAndFailsClosed(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 17, 16, 0, 0, 0, time.UTC)
	deleteErr := errors.New("forced reaper deletion failure")
	client := &fakeDropletClient{
		droplets: []godo.Droplet{
			{ID: 101, Name: "first-orphan", Created: now.Add(-2 * time.Hour).Format(time.RFC3339)},
			{ID: 202, Name: "second-orphan", Created: now.Add(-2 * time.Hour).Format(time.RFC3339)},
		},
		deleteErrors: map[int]error{101: deleteErr},
	}

	err := reap(context.Background(), client, now, time.Hour, &bytes.Buffer{})
	if !errors.Is(err, deleteErr) {
		t.Fatalf("reaper error = %v, want %v", err, deleteErr)
	}
	if !slices.Equal(client.deletedIDs, []int{101, 202}) {
		t.Fatalf("reaper stopped after first cleanup failure: %v", client.deletedIDs)
	}
}
