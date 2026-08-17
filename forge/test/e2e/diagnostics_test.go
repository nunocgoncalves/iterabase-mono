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
// If a bootstrap pod exists, its complete init-container log must expose the
// expected admin and token credentials in the reviewed format. Inspection is
// deliberately broader than the shared collector's retained tail so no older
// literal can be missed before generic pod logs are persisted.
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
	pods, err := sshOutput(client, "sudo k3s kubectl get pods -n iterabase-system -l app.kubernetes.io/component=api -o name")
	if err != nil {
		t.Logf("skip shared pod-log diagnostics because bootstrap pod presence cannot be inspected: %v", err)
		return false
	}
	if strings.TrimSpace(pods) == "" {
		return true
	}
	output, err := sshOutput(client, bootstrapCredentialLogCommand())
	if err != nil {
		t.Logf("skip shared pod-log diagnostics because bootstrap credentials cannot be inspected: %v", err)
		return false
	}
	secrets, err := bootstrapSecretLiterals(output)
	if err != nil {
		t.Logf("skip shared pod-log diagnostics because bootstrap credential evidence is incomplete: %v", err)
		return false
	}
	diagnostics.redactor.Add(secrets...)
	return true
}

func bootstrapCredentialLogCommand() string {
	return "sudo k3s kubectl logs -n iterabase-system -l app.kubernetes.io/component=api -c bootstrap --tail=-1 --max-log-requests=20"
}

func bootstrapSecretLiterals(output string) ([]string, error) {
	matches := keyRe.FindAllStringSubmatch(output, -1)
	found := make(map[string]bool)
	secrets := make([]string, 0, len(matches))
	for _, match := range matches {
		if len(match) <= 2 {
			continue
		}
		found[match[1]] = true
		secrets = append(secrets, match[2])
	}
	missing := make([]string, 0, 2)
	for _, scope := range []string{"scope=admin", "scope=token"} {
		if !found[scope] {
			missing = append(missing, scope)
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("missing expected bootstrap credential scopes %s", strings.Join(missing, ", "))
	}
	return secrets, nil
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

func TestBootstrapSecretLiteralsRequireExpectedCredentialShapes(t *testing.T) {
	t.Parallel()
	valid := "Admin API key (scope=admin): admin-secret\n" +
		"Service account API key (scope=token): token-secret\n"
	secrets, err := bootstrapSecretLiterals(valid)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(secrets, ",") != "admin-secret,token-secret" {
		t.Fatalf("bootstrap secrets = %v", secrets)
	}

	changed := "Admin bootstrap credential (scope=admin) => changed-admin-secret\n" +
		"Service account API key (scope=token): token-secret\n"
	if secrets, err = bootstrapSecretLiterals(changed); err == nil || secrets != nil {
		t.Fatalf("changed bootstrap credential format accepted: secrets=%v err=%v", secrets, err)
	} else if strings.Contains(err.Error(), "changed-admin-secret") {
		t.Fatalf("bootstrap parse error leaked credential: %v", err)
	}
}

func TestBootstrapCredentialInspectionCoversCollectorLogRange(t *testing.T) {
	t.Parallel()
	if !strings.Contains(bootstrapCredentialLogCommand(), "--tail=-1") {
		t.Fatalf("bootstrap inspection must read the complete log: %s", bootstrapCredentialLogCommand())
	}
	output := "Admin API key (scope=admin): old-admin-secret\n" +
		"Service account API key (scope=token): old-token-secret\n" +
		strings.Repeat("later log line\n", shareddiagnostics.PodLogTailLines+50)
	secrets, err := bootstrapSecretLiterals(output)
	if err != nil {
		t.Fatal(err)
	}
	if len(secrets) != 2 {
		t.Fatalf("bootstrap secrets outside retained tail were not registered: %v", secrets)
	}
}
