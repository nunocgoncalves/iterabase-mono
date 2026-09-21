package cli

import (
	"bytes"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"

	"github.com/nunocgoncalves/iterabase-mono/forge/internal/config"
	"github.com/nunocgoncalves/iterabase-mono/forge/internal/lifecycle"
	"github.com/nunocgoncalves/iterabase-mono/forge/internal/provisioner"
)

func readyHostInotifyState() *provisioner.HostInotifyState {
	return &provisioner.HostInotifyState{
		Effective:       provisioner.InotifyMaxUserInstancesRequired,
		Persisted:       provisioner.InotifyMaxUserInstancesRequired,
		DropInPresent:   true,
		DropInRegular:   true,
		DropInOwner:     "0:0",
		DropInMode:      "644",
		DropInCanonical: true,
	}
}

func TestPrintApplyResult_HostInotify(t *testing.T) {
	var out bytes.Buffer
	cfg := &config.Cluster{Metadata: config.Metadata{Name: "opo1"}}
	res := &lifecycle.Result{Plan: &lifecycle.ReconcilePlan{}, HostInotify: readyHostInotifyState()}
	printApplyResult(&out, cfg, res)
	assert.Contains(t, out.String(), "inotify:    ready (effective=8192 required=8192 persisted=8192 drop-in present regular=true owner=0:0 mode=644 canonical=true)")
}

func TestPrintApplyResult_HostInotifyDrift(t *testing.T) {
	var out bytes.Buffer
	cfg := &config.Cluster{Metadata: config.Metadata{Name: "opo1"}}
	res := &lifecycle.Result{Plan: &lifecycle.ReconcilePlan{}, HostInotify: &provisioner.HostInotifyState{Effective: 128}}
	printApplyResult(&out, cfg, res)
	assert.Contains(t, out.String(), "inotify:    drift (effective=128 required=8192 persisted=0 drop-in missing)")
}

func TestPrintApplyResult_OmitsHostInotifyWhenNotObserved(t *testing.T) {
	var out bytes.Buffer
	printApplyResult(&out, &config.Cluster{Metadata: config.Metadata{Name: "opo1"}}, &lifecycle.Result{Plan: &lifecycle.ReconcilePlan{}})
	assert.NotContains(t, out.String(), "inotify")
}

func TestPrintPlanIncludesHostInotifyEvidence(t *testing.T) {
	cmd := &cobra.Command{}
	var out bytes.Buffer
	cmd.SetOut(&out)
	printPlan(cmd, &lifecycle.ReconcilePlan{
		Preflight:   &provisioner.PreflightResult{},
		Action:      lifecycle.ActionInstall,
		WantVersion: "v1.34.10+k3s1",
		HostInotify: readyHostInotifyState(),
	})
	s := out.String()
	assert.Contains(t, s, "action:    install")
	assert.Contains(t, s, "inotify:   ready (effective=8192 required=8192 persisted=8192 drop-in present regular=true owner=0:0 mode=644 canonical=true)")
}

func TestPrintHostInotifyStatus(t *testing.T) {
	var out bytes.Buffer
	printHostInotifyStatus(&out, &provisioner.HostInotifyState{Effective: 128})
	assert.Equal(t, "inotify:    drift (effective=128 required=8192 persisted=0 drop-in missing)\n", out.String())

	out.Reset()
	printHostInotifyStatus(&out, nil)
	assert.Empty(t, out.String())
}

func TestHostInotifySummaryUnavailable(t *testing.T) {
	assert.Equal(t, "unavailable", hostInotifySummary(nil))
}
