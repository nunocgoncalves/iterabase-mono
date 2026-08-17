package e2e

import (
	"context"
	"errors"
	"testing"

	"github.com/digitalocean/godo"
)

type fakeCPUVMProvisioner struct {
	droplet      *godo.Droplet
	publicIPErr  error
	destroyErr   error
	destroyedIDs []int
}

func (provisioner *fakeCPUVMProvisioner) Create(context.Context, string, string) (*godo.Droplet, error) {
	return provisioner.droplet, nil
}

func (provisioner *fakeCPUVMProvisioner) PublicIP(context.Context, int) (string, error) {
	return "", provisioner.publicIPErr
}

func (provisioner *fakeCPUVMProvisioner) Destroy(_ context.Context, id int) error {
	provisioner.destroyedIDs = append(provisioner.destroyedIDs, id)
	return provisioner.destroyErr
}

func TestCPUProvisioningAndCleanupFailuresRetainReaperOwnership(t *testing.T) {
	t.Parallel()
	provisionErr := errors.New("forced public IP failure")
	cleanupErr := errors.New("forced CPU deletion failure")
	provisioner := &fakeCPUVMProvisioner{
		droplet:     &godo.Droplet{ID: 101, Tags: []string{"forge-e2e", "fixture-run"}},
		publicIPErr: provisionErr,
		destroyErr:  cleanupErr,
	}
	state := &digitalOceanCPUState{
		ctx: context.Background(), provisioner: provisioner, runID: "fixture-run",
		ready: func(context.Context, string, string) error { return nil },
	}

	if err := provisionCPUHost(state); !errors.Is(err, provisionErr) {
		t.Fatalf("provisioning error = %v, want %v", err, provisionErr)
	}
	if state.droplet == nil || state.droplet.ID != 101 {
		t.Fatalf("accepted CPU resource identity was not retained: %+v", state.droplet)
	}
	if !hasResourceTag(state.droplet.Tags, "forge-e2e") {
		t.Fatalf("failed CPU resource has no reaper tag: %v", state.droplet.Tags)
	}
	if err := state.destroyCPUHost(); !errors.Is(err, cleanupErr) {
		t.Fatalf("cleanup error = %v, want %v", err, cleanupErr)
	}
	if len(provisioner.destroyedIDs) != 1 || provisioner.destroyedIDs[0] != state.droplet.ID {
		t.Fatalf("CPU cleanup did not target retained resource: %v", provisioner.destroyedIDs)
	}
}

type fakeGPUVMProvisioner struct {
	vm           *GPUVM
	provisionErr error
	destroyErr   error
	destroyedIDs []int
}

func (provisioner *fakeGPUVMProvisioner) Provision(context.Context, string, string, string) (*GPUVM, error) {
	return provisioner.vm, provisioner.provisionErr
}

func (provisioner *fakeGPUVMProvisioner) Destroy(_ context.Context, id int) error {
	provisioner.destroyedIDs = append(provisioner.destroyedIDs, id)
	return provisioner.destroyErr
}

func TestGPUProvisioningAndCleanupFailuresRetainReaperOwnership(t *testing.T) {
	t.Parallel()
	provisionErr := errors.New("forced post-create provisioning failure")
	cleanupErr := errors.New("forced GPU deletion failure")
	provisioner := &fakeGPUVMProvisioner{
		vm:           &GPUVM{ID: 202, Tags: []string{"forge-e2e", "forge-gpu-e2e", "fixture-run"}},
		provisionErr: provisionErr,
		destroyErr:   cleanupErr,
	}
	state := &digitalOceanGPUState{ctx: context.Background(), provisioner: provisioner, runID: "fixture-run"}

	if err := provisionGPUHost(state); !errors.Is(err, provisionErr) {
		t.Fatalf("provisioning error = %v, want %v", err, provisionErr)
	}
	if state.vm == nil || state.vm.ID != 202 {
		t.Fatalf("accepted GPU resource identity was not retained: %+v", state.vm)
	}
	if !hasResourceTag(state.vm.Tags, "forge-e2e") {
		t.Fatalf("failed GPU resource has no reaper tag: %v", state.vm.Tags)
	}
	if err := state.destroyGPUHost(); !errors.Is(err, cleanupErr) {
		t.Fatalf("cleanup error = %v, want %v", err, cleanupErr)
	}
	if len(provisioner.destroyedIDs) != 1 || provisioner.destroyedIDs[0] != state.vm.ID {
		t.Fatalf("GPU cleanup did not target retained resource: %v", provisioner.destroyedIDs)
	}
}

func hasResourceTag(tags []string, want string) bool {
	for _, tag := range tags {
		if tag == want {
			return true
		}
	}
	return false
}
