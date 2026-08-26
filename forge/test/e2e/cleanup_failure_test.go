package e2e

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/digitalocean/godo"
	sharede2e "github.com/nunocgoncalves/iterabase-mono/testkit/e2e"
)

type fakeCPUVMProvisioner struct {
	droplet           *godo.Droplet
	publicIPErr       error
	destroyErr        error
	destroyRecordPath string
	destroyedIDs      []int
}

func (provisioner *fakeCPUVMProvisioner) Create(context.Context, string, string) (*godo.Droplet, error) {
	return provisioner.droplet, nil
}

func (provisioner *fakeCPUVMProvisioner) PublicIP(context.Context, int) (string, error) {
	return "", provisioner.publicIPErr
}

func (provisioner *fakeCPUVMProvisioner) Destroy(_ context.Context, id int) error {
	provisioner.destroyedIDs = append(provisioner.destroyedIDs, id)
	recordErr := writeDestroyRecord(provisioner.destroyRecordPath, id, provisioner.droplet.Tags)
	return errors.Join(provisioner.destroyErr, recordErr)
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
	vm                *GPUVM
	provisionErr      error
	destroyErr        error
	destroyRecordPath string
	destroyedIDs      []int
}

func (provisioner *fakeGPUVMProvisioner) Provision(context.Context, string, string, string) (*GPUVM, error) {
	return provisioner.vm, provisioner.provisionErr
}

func (provisioner *fakeGPUVMProvisioner) Destroy(_ context.Context, id int) error {
	provisioner.destroyedIDs = append(provisioner.destroyedIDs, id)
	recordErr := writeDestroyRecord(provisioner.destroyRecordPath, id, provisioner.vm.Tags)
	return errors.Join(provisioner.destroyErr, recordErr)
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

func TestDedicatedRWXVolumeRequestRegistersReaperOwnership(t *testing.T) {
	t.Parallel()
	request := newRWXVolumeRequest("fixture-run", "fixture-node")
	if request.Name != "fixture-node-ssd" || !hasResourceTag(request.Tags, "forge-e2e") || !hasResourceTag(request.Tags, "fixture-run") {
		t.Fatalf("RWX volume request does not preserve tagged reaper ownership: name=%q tags=%v", request.Name, request.Tags)
	}
	if request.SizeGigaBytes != 25 || request.Region != region {
		t.Fatalf("RWX volume request does not preserve dedicated capacity/region: %+v", request)
	}
}

func TestCPUScenarioSizeReservesManagedStorageHeadroom(t *testing.T) {
	t.Parallel()
	if got := cpuScenarioSize(false); got != size {
		t.Fatalf("external CPU scenario size=%q want=%q", got, size)
	}
	if got := cpuScenarioSize(true); got != managedSize {
		t.Fatalf("managed CPU scenario size=%q want=%q", got, managedSize)
	}
}

func TestProvisioningRequestsRegisterReaperOwnership(t *testing.T) {
	t.Parallel()
	const runID = "fixture-run"
	requests := map[string]*godo.DropletCreateRequest{
		"cpu": newCPUDropletRequest(runID, "public-key"),
		"gpu": newGPUDropletRequest(runID, "public-key", "gpu-region", "gpu-size"),
	}
	for name, request := range requests {
		name, request := name, request
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if request.Name != runID || !hasResourceTag(request.Tags, "forge-e2e") || !hasResourceTag(request.Tags, runID) {
				t.Fatalf("%s provisioning request does not preserve tagged reaper ownership: name=%q tags=%v", name, request.Name, request.Tags)
			}
		})
	}
}

func TestProvisioningFailuresRunOwnerCleanupHooks(t *testing.T) {
	const helperEnv = "FORGE_E2E_CLEANUP_FAILURE_HELPER"
	if os.Getenv(helperEnv) == "1" {
		runOwnerCleanupFailureHelper(t)
		return
	}

	directory := t.TempDir()
	command := exec.Command(os.Args[0], "-test.run=^TestProvisioningFailuresRunOwnerCleanupHooks$")
	command.Env = append(os.Environ(), helperEnv+"=1", "FORGE_E2E_CLEANUP_FAILURE_DIR="+directory)
	output, err := command.CombinedOutput()
	if err == nil {
		t.Fatalf("forced owner provisioning/cleanup failures unexpectedly passed:\n%s", output)
	}
	for name, want := range map[string]string{
		"cpu-destroyed": "101 forge-e2e,fixture-run",
		"gpu-destroyed": "202 forge-e2e,forge-gpu-e2e,fixture-run",
	} {
		contents, readErr := os.ReadFile(filepath.Join(directory, name))
		if readErr != nil {
			t.Fatalf("owner cleanup hook did not record %s: %v\n%s", name, readErr, output)
		}
		if strings.TrimSpace(string(contents)) != want {
			t.Fatalf("%s cleanup record = %q, want %q", name, contents, want)
		}
	}
	for _, want := range []string{
		"wait for droplet IP: forced public IP failure",
		"forced post-create provisioning failure",
		"tagged reaper remains the crash-safety fallback",
	} {
		if !strings.Contains(string(output), want) {
			t.Fatalf("owner lifecycle evidence does not contain %q:\n%s", want, output)
		}
	}
}

func runOwnerCleanupFailureHelper(t *testing.T) {
	const runID = "fixture-run"
	directory := os.Getenv("FORGE_E2E_CLEANUP_FAILURE_DIR")
	cpuRequest := newCPUDropletRequest(runID, "public-key")
	gpuRequest := newGPUDropletRequest(runID, "public-key", "gpu-region", "gpu-size")
	cpuProvisioner := &fakeCPUVMProvisioner{
		droplet:           &godo.Droplet{ID: 101, Tags: cpuRequest.Tags},
		publicIPErr:       errors.New("forced public IP failure"),
		destroyErr:        errors.New("forced CPU deletion failure"),
		destroyRecordPath: filepath.Join(directory, "cpu-destroyed"),
	}
	gpuProvisioner := &fakeGPUVMProvisioner{
		vm: &GPUVM{
			ID: 202, Tags: gpuRequest.Tags,
		},
		provisionErr:      errors.New("forced post-create provisioning failure"),
		destroyErr:        errors.New("forced GPU deletion failure"),
		destroyRecordPath: filepath.Join(directory, "gpu-destroyed"),
	}

	suite := sharede2e.NewSuite(
		sharede2e.SuiteMetadata{Name: "forge-cleanup", Owner: "forge", Entrypoint: "forge/test/e2e"},
		func(*testing.T) sharede2e.Fixture {
			return sharede2e.Fixture{Mode: sharede2e.FixtureSource, SourceSHA: strings.Repeat("a", 40)}
		},
	)
	suite.Add(
		sharede2e.Define(sharede2e.Scenario[*digitalOceanCPUState]{
			Metadata: sharede2e.ScenarioMetadata{
				Name: "cpu-provisioning-failure", Description: "Forces CPU provisioning and owner cleanup failure.",
				Tier: sharede2e.TierF0, FixtureModes: []sharede2e.FixtureMode{sharede2e.FixtureSource},
			},
			NewState: func(*testing.T) *digitalOceanCPUState {
				return &digitalOceanCPUState{
					ctx: context.Background(), provisioner: cpuProvisioner, runID: runID,
					ready: func(context.Context, string, string) error { return nil },
				}
			},
			Stages: []sharede2e.Stage[*digitalOceanCPUState]{{Name: "provision-host", Run: func(t *testing.T, state *digitalOceanCPUState) {
				if err := provisionCPUHost(state); err != nil {
					t.Fatal(err)
				}
			}}},
			Cleanup: cpuScenarioCleanup(),
		}),
		sharede2e.Define(sharede2e.Scenario[*digitalOceanGPUState]{
			Metadata: sharede2e.ScenarioMetadata{
				Name: "gpu-provisioning-failure", Description: "Forces GPU provisioning and owner cleanup failure.",
				Tier: sharede2e.TierF0, FixtureModes: []sharede2e.FixtureMode{sharede2e.FixtureSource},
			},
			NewState: func(*testing.T) *digitalOceanGPUState {
				return &digitalOceanGPUState{ctx: context.Background(), provisioner: gpuProvisioner, runID: runID}
			},
			Stages: []sharede2e.Stage[*digitalOceanGPUState]{{Name: "provision-host", Run: func(t *testing.T, state *digitalOceanGPUState) {
				if err := provisionGPUHost(state); err != nil {
					t.Fatal(err)
				}
			}}},
			Cleanup: gpuScenarioCleanup(),
		}),
	)
	suite.Run(t)
}

func writeDestroyRecord(path string, id int, tags []string) error {
	if path == "" {
		return nil
	}
	return os.WriteFile(path, []byte(fmt.Sprintf("%d %s\n", id, strings.Join(tags, ","))), 0o600)
}

func hasResourceTag(tags []string, want string) bool {
	for _, tag := range tags {
		if tag == want {
			return true
		}
	}
	return false
}
