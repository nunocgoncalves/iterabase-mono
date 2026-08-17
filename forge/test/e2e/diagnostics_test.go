package e2e

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sharede2e "github.com/nunocgoncalves/iterabase-mono/testkit/e2e"
	shareddiagnostics "github.com/nunocgoncalves/iterabase-mono/testkit/e2e/diagnostics"
	"github.com/nunocgoncalves/iterabase-mono/testkit/e2e/process"
	"github.com/nunocgoncalves/iterabase-mono/testkit/e2e/redact"
)

const (
	failureDomainProvisioning   = "provisioning"
	failureDomainSubstrate      = "forge-substrate"
	failureDomainForgeReconcile = "forge-reconciliation"
	failureDomainForgeHandoff   = "forge-artifact-handoff"
	failureDomainDependentSmoke = "dependent-layer-smoke"
	failureDomainCleanup        = "cloud-cleanup"
)

type forgeDiagnostics struct {
	domain    string
	outputDir string
	redactor  *redact.Redactor
}

func newForgeDiagnostics(t *testing.T, scenario string) forgeDiagnostics {
	t.Helper()
	outputDir := os.Getenv("ITERABASE_E2E_DIAGNOSTICS")
	if outputDir == "" {
		outputDir = t.TempDir()
	} else {
		outputDir = filepath.Join(outputDir, scenario)
	}
	absolute, err := filepath.Abs(outputDir)
	if err != nil {
		t.Fatalf("resolve Forge diagnostics directory: %v", err)
	}
	if err := os.MkdirAll(absolute, 0o700); err != nil {
		t.Fatalf("create Forge diagnostics directory: %v", err)
	}
	return forgeDiagnostics{domain: failureDomainProvisioning, outputDir: absolute, redactor: redact.New()}
}

func (diagnostics *forgeDiagnostics) setDomain(domain string) {
	diagnostics.domain = domain
}

func (diagnostics *forgeDiagnostics) recordDomain(t *testing.T) {
	t.Helper()
	path := filepath.Join(diagnostics.outputDir, "failure-domain.txt")
	if err := os.WriteFile(path, []byte(diagnostics.domain+"\n"), 0o600); err != nil {
		t.Logf("write failure domain: %v", err)
	}
	t.Logf("Forge failure domain: %s", diagnostics.domain)
}

func (diagnostics *forgeDiagnostics) collectSSH(t *testing.T, ip, keyPath string, commands map[string]string) {
	t.Helper()
	if ip == "" {
		return
	}
	client, err := sshDial(ip, keyPath)
	if err != nil {
		t.Logf("SSH diagnostics unavailable for %s: %v", ip, err)
		return
	}
	defer client.Close()
	for name, command := range commands {
		output, commandErr := sshOutput(client, command)
		redacted := diagnostics.redactor.String(output)
		path := filepath.Join(diagnostics.outputDir, "remote-"+name+".log")
		if err := os.WriteFile(path, []byte(redacted), 0o600); err != nil {
			t.Logf("write remote diagnostic %s: %v", name, err)
		}
		if commandErr != nil {
			t.Logf("remote diagnostic %s: %v", name, commandErr)
		}
	}
}

// registerBootstrapSecrets returns whether full pod-log collection is safe.
// If bootstrap logs cannot be inspected or their credential shape changes, the
// shared collector is skipped rather than risking retention of an unknown key.
func (diagnostics *forgeDiagnostics) registerBootstrapSecrets(t *testing.T, ip, keyPath string) bool {
	t.Helper()
	if ip == "" {
		return false
	}
	client, err := sshDial(ip, keyPath)
	if err != nil {
		t.Logf("skip shared pod-log diagnostics because bootstrap credentials cannot be inspected: %v", err)
		return false
	}
	defer client.Close()
	output, err := sshOutput(client, "sudo k3s kubectl logs -n iterabase-system -l app.kubernetes.io/component=api -c bootstrap --tail=200")
	if err != nil {
		t.Logf("skip shared pod-log diagnostics because bootstrap credentials cannot be inspected: %v", err)
		return false
	}
	matches := keyRe.FindAllStringSubmatch(output, -1)
	if strings.Contains(strings.ToLower(output), "api key") && len(matches) == 0 {
		t.Log("skip shared pod-log diagnostics because bootstrap credential format is unrecognized")
		return false
	}
	for _, match := range matches {
		if len(match) > 2 {
			diagnostics.redactor.Add(match[2])
		}
	}
	return true
}

func (diagnostics *forgeDiagnostics) collectSharedCluster(t *testing.T, kubeconfig string) {
	t.Helper()
	if _, err := os.Stat(kubeconfig); err != nil {
		t.Logf("shared cluster diagnostics unavailable: %v", err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()
	runner := process.Runner{Redactor: diagnostics.redactor, OutputDir: filepath.Join(diagnostics.outputDir, "process")}
	err := (shareddiagnostics.Collector{
		Executor: runner, Kubeconfig: kubeconfig,
		OutputDir: filepath.Join(diagnostics.outputDir, "shared-cluster"), Redactor: diagnostics.redactor,
	}).Collect(ctx)
	if err != nil {
		t.Logf("best-effort shared cluster diagnostics: %v", err)
	}
}

func cpuDiagnosticStage(domain string, run func(*testing.T, *digitalOceanCPUState)) func(*testing.T, *digitalOceanCPUState) {
	return func(t *testing.T, state *digitalOceanCPUState) {
		state.diagnostics.setDomain(domain)
		run(t, state)
	}
}

func gpuDiagnosticStage(domain string, run func(*testing.T, *digitalOceanGPUState)) func(*testing.T, *digitalOceanGPUState) {
	return func(t *testing.T, state *digitalOceanGPUState) {
		state.diagnostics.setDomain(domain)
		run(t, state)
	}
}

func collectCPUDiagnostics(t *testing.T, state *digitalOceanCPUState) {
	t.Helper()
	state.diagnostics.recordDomain(t)
	safeClusterLogs := state.diagnostics.registerBootstrapSecrets(t, state.ip, state.privKeyPath)
	state.diagnostics.collectSSH(t, state.ip, state.privKeyPath, map[string]string{
		"provisioning": "cloud-init status --long 2>&1; systemctl --no-pager --full status k3s 2>&1 || true",
		"forge-state":  fmt.Sprintf("sudo ls -la /var/lib/forge/overlay/%s 2>&1 || true; sudo k3s kubectl get gitrepositories,kustomizations -A -o wide 2>&1 || true", state.runID),
	})
	if safeClusterLogs {
		state.diagnostics.collectSharedCluster(t, filepath.Join(state.forgeHome, state.runID, "kubeconfig.yaml"))
	}
}

func collectGPUDiagnostics(t *testing.T, state *digitalOceanGPUState) {
	t.Helper()
	state.diagnostics.recordDomain(t)
	if state.vm == nil {
		return
	}
	safeClusterLogs := state.diagnostics.registerBootstrapSecrets(t, state.vm.IP, state.privKeyPath)
	state.diagnostics.collectSSH(t, state.vm.IP, state.privKeyPath, map[string]string{
		"provisioning": "cloud-init status --long 2>&1; systemctl --no-pager --full status k3s 2>&1 || true",
		"gpu-policy":   "sudo k3s kubectl get clusterpolicy -o yaml 2>&1 || true; sudo k3s kubectl get nodes -o wide --show-labels 2>&1 || true",
		"gpu-workload": "sudo k3s kubectl get daemonsets,pods -n gpu-operator -o wide 2>&1 || true; sudo k3s kubectl get deployment,pods,pvc -n forge-gpu-upgrade -o wide 2>&1 || true",
	})
	dumpGPUDiagnostics(t, state.vm.IP, state.privKeyPath)
	if safeClusterLogs {
		state.diagnostics.collectSharedCluster(t, filepath.Join(state.forgeHome, state.runID, "kubeconfig.yaml"))
	}
}

func cpuScenarioDiagnostics() []sharede2e.Hook[*digitalOceanCPUState] {
	return []sharede2e.Hook[*digitalOceanCPUState]{{Name: "shared-failure-evidence", Run: collectCPUDiagnostics}}
}

func cpuScenarioCleanup() []sharede2e.Hook[*digitalOceanCPUState] {
	return []sharede2e.Hook[*digitalOceanCPUState]{{Name: "destroy-cloud-host", Run: func(t *testing.T, state *digitalOceanCPUState) { state.cleanup(t) }}}
}

func gpuScenarioDiagnostics() []sharede2e.Hook[*digitalOceanGPUState] {
	return []sharede2e.Hook[*digitalOceanGPUState]{{Name: "shared-failure-evidence", Run: collectGPUDiagnostics}}
}

func gpuScenarioCleanup() []sharede2e.Hook[*digitalOceanGPUState] {
	return []sharede2e.Hook[*digitalOceanGPUState]{{Name: "destroy-cloud-host", Run: func(t *testing.T, state *digitalOceanGPUState) { state.cleanup(t) }}}
}

func TestForgeDiagnosticsRecordsFailureDomain(t *testing.T) {
	t.Setenv("ITERABASE_E2E_DIAGNOSTICS", t.TempDir())
	diagnostics := newForgeDiagnostics(t, "fixture")
	diagnostics.setDomain(failureDomainDependentSmoke)
	diagnostics.recordDomain(t)
	contents, err := os.ReadFile(filepath.Join(diagnostics.outputDir, "failure-domain.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(contents)) != failureDomainDependentSmoke {
		t.Fatalf("failure domain evidence = %q", contents)
	}
}
