package e2e

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
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

type shareManagerAttemptWatcher struct {
	diagnostics *forgeDiagnostics
	ip          string
	keyPath     string
	pid         string
	remoteDir   string
	label       string
	stopped     bool
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
// If a bootstrap pod exists, every current and retained previous bootstrap log
// must expose the expected admin and token credentials in the reviewed format.
// Inspection reads complete logs, which is deliberately broader than the shared
// collector's retained tail, before generic current/previous logs are persisted.
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
	pods, err := sshOutput(client, bootstrapPodStatusCommand())
	if err != nil {
		t.Logf("skip shared pod-log diagnostics because bootstrap pod presence cannot be inspected: %v", err)
		return false
	}
	requests, err := bootstrapLogRequests(pods)
	if err != nil {
		t.Logf("skip shared pod-log diagnostics because bootstrap pod status cannot be inspected: %v", err)
		return false
	}
	secrets := make([]string, 0, len(requests)*2)
	for _, request := range requests {
		output, outputErr := sshOutput(client, bootstrapCredentialLogCommand(request))
		if outputErr != nil {
			t.Logf("skip shared pod-log diagnostics because %s bootstrap credentials cannot be inspected: %v", request.source(), outputErr)
			return false
		}
		literals, literalErr := bootstrapSecretLiterals(output)
		if literalErr != nil {
			t.Logf("skip shared pod-log diagnostics because %s bootstrap credential evidence is incomplete: %v", request.source(), literalErr)
			return false
		}
		secrets = append(secrets, literals...)
	}
	diagnostics.redactor.Add(secrets...)
	return true
}

type bootstrapLogRequest struct {
	pod      string
	previous bool
}

func (request bootstrapLogRequest) source() string {
	if request.previous {
		return "previous"
	}
	return "current"
}

func bootstrapPodStatusCommand() string {
	return "sudo k3s kubectl get pods -n iterabase-system -l app.kubernetes.io/component=api -o json"
}

func bootstrapLogRequests(output string) ([]bootstrapLogRequest, error) {
	var pods struct {
		Items []struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
			Status struct {
				InitContainerStatuses []struct {
					Name         string `json:"name"`
					RestartCount int32  `json:"restartCount"`
				} `json:"initContainerStatuses"`
			} `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(output), &pods); err != nil {
		return nil, fmt.Errorf("decode bootstrap pod status: %w", err)
	}
	requests := make([]bootstrapLogRequest, 0, len(pods.Items)*2)
	for _, pod := range pods.Items {
		if pod.Metadata.Name == "" {
			return nil, fmt.Errorf("bootstrap pod status has no name")
		}
		requests = append(requests, bootstrapLogRequest{pod: pod.Metadata.Name})
		for _, status := range pod.Status.InitContainerStatuses {
			if status.Name == "bootstrap" && status.RestartCount > 0 {
				requests = append(requests, bootstrapLogRequest{pod: pod.Metadata.Name, previous: true})
				break
			}
		}
	}
	return requests, nil
}

func bootstrapCredentialLogCommand(request bootstrapLogRequest) string {
	previous := ""
	if request.previous {
		previous = " --previous"
	}
	return fmt.Sprintf("sudo k3s kubectl logs -n iterabase-system %q -c bootstrap%s --tail=-1", request.pod, previous)
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

func startShareManagerAttemptWatcher(t *testing.T, diagnostics *forgeDiagnostics, ip, keyPath, label string) func() {
	t.Helper()
	client, err := sshDial(ip, keyPath)
	if err != nil {
		t.Fatalf("start share-manager attempt watcher: dial host: %v", err)
	}
	defer client.Close()

	remoteBase := fmt.Sprintf("/tmp/iterabase-share-manager-attempts-%d", time.Now().UnixNano())
	remoteScript := remoteBase + ".sh"
	remoteDir := remoteBase + ".d"
	script := shareManagerAttemptWatcherScript()
	encoded := base64.StdEncoding.EncodeToString([]byte(script))
	start := fmt.Sprintf(
		"sudo mkdir -p %s; printf %%s %s | base64 --decode | sudo tee %s >/dev/null; sudo chmod 0700 %s; sudo sh -c 'nohup \"$1\" \"$2\" >\"$2/driver.log\" 2>&1 </dev/null & echo $!' _ %s %s",
		candidateShellQuote(remoteDir), candidateShellQuote(encoded), candidateShellQuote(remoteScript), candidateShellQuote(remoteScript),
		candidateShellQuote(remoteScript), candidateShellQuote(remoteDir),
	)
	output, err := sshOutput(client, start)
	if err != nil {
		t.Fatalf("start share-manager attempt watcher: %v\n%s", err, output)
	}
	pid := strings.TrimSpace(output)
	pidNumber, pidErr := strconv.Atoi(pid)
	if pidErr != nil || pidNumber <= 0 {
		t.Fatalf("share-manager attempt watcher returned invalid PID %q", pid)
	}
	readyCommand := fmt.Sprintf(
		"for i in $(seq 1 100); do test -f %s/ready && exit 0; sleep 0.1; done; sudo cat %s/driver.log >&2; exit 1",
		candidateShellQuote(remoteDir), candidateShellQuote(remoteDir),
	)
	if readyOutput, readyErr := sshOutput(client, readyCommand); readyErr != nil {
		t.Fatalf("share-manager attempt watcher did not establish event watches and baseline network evidence: %v\n%s", readyErr, readyOutput)
	}
	watcher := &shareManagerAttemptWatcher{
		diagnostics: diagnostics, ip: ip, keyPath: keyPath, pid: pid,
		remoteDir: remoteDir, label: label,
	}
	return func() { watcher.stop(t) }
}

func shareManagerAttemptWatcherScript() string {
	return `#!/usr/bin/env bash
set -uo pipefail
output_dir=${1:?output directory required}
driver="$output_dir/driver.log"
child_pids="$output_dir/child-pids"
mkdir -p "$output_dir"
: >"$child_pids"

capture_network_state() {
  local reason=${1:?snapshot reason required}
  {
    printf '\n===== %s network snapshot: %s =====\n' "$(date -u +%FT%TZ.%N)" "$reason"
    echo '--- recovery-backend NetworkPolicy ---'
    k3s kubectl get networkpolicy longhorn-recovery-backend -n longhorn-system -o yaml || true
    echo '--- recovery-backend Service and EndpointSlices ---'
    k3s kubectl get service longhorn-recovery-backend -n longhorn-system -o wide || true
    k3s kubectl get endpointslices.discovery.k8s.io -n longhorn-system -l kubernetes.io/service-name=longhorn-recovery-backend -o yaml || true
    echo '--- share-manager pods ---'
    k3s kubectl get pods -n longhorn-system -l longhorn.io/component=share-manager -o wide || true
    echo '--- TCP/9503 sockets ---'
    ss -ntp 2>/dev/null | grep ':9503' || true
    echo '--- iptables filter counters ---'
    if command -v iptables-save >/dev/null 2>&1; then
      iptables-save -c -t filter || true
    fi
    echo '--- kube-router ipsets ---'
    if command -v ipset >/dev/null 2>&1; then
      ipset save || true
    fi
  } >>"$output_dir/network-snapshots.log" 2>&1
}

watch_table() {
  local stream=${1:?stream required}
  local file=${2:?file required}
  shift 2
  while true; do
    k3s kubectl get "$@" --watch --output-watch-events --no-headers 2>>"$driver" |
      while IFS= read -r event; do
        printf '%s|%s|%s\n' "$(date -u +%FT%TZ.%N)" "$stream" "$event"
      done >>"$file"
    printf '%s watch stream %s closed; reconnecting\n' "$(date -u +%FT%TZ.%N)" "$stream" >>"$driver"
    sleep 1
  done
}

follow_share_manager_logs() {
  local pod_name=${1:?pod name required}
  local pod_uid=${2:?pod uid required}
  local file="$output_dir/share-manager-${pod_uid}-follow.log"
  while [[ "$(k3s kubectl get pod "$pod_name" -n longhorn-system -o jsonpath='{.metadata.uid}' 2>/dev/null || true)" == "$pod_uid" ]]; do
    {
      printf '\n===== %s following %s uid=%s =====\n' "$(date -u +%FT%TZ.%N)" "$pod_name" "$pod_uid"
      if k3s kubectl logs -n longhorn-system "$pod_name" -c share-manager --follow --timestamps; then
        exit 0
      fi
    } >>"$file" 2>&1
    sleep 1
  done
}

capture_terminal_pod() {
  local event_type=${1:?event type required}
  local pod_name=${2:?pod name required}
  local pod_uid=${3:?pod uid required}
  local event=${4:?event required}
  local file="$output_dir/share-manager-${pod_uid}-terminal.log"
  {
    printf '\n===== %s %s %s uid=%s =====\n' "$(date -u +%FT%TZ.%N)" "$event_type" "$pod_name" "$pod_uid"
    printf '%s\n' "$event"
    echo '--- final live pod object, if retained ---'
    k3s kubectl get pod "$pod_name" -n longhorn-system -o yaml || true
    echo '--- current share-manager log, if retained ---'
    k3s kubectl logs -n longhorn-system "$pod_name" -c share-manager --tail=500 --timestamps || true
    echo '--- previous share-manager log, if retained ---'
    k3s kubectl logs -n longhorn-system "$pod_name" -c share-manager --previous --tail=500 --timestamps || true
  } >>"$file" 2>&1
}

watch_share_manager_pods() {
  while true; do
    k3s kubectl get pods -n longhorn-system -l longhorn.io/component=share-manager \
      --watch --output-watch-events --no-headers \
      -o 'custom-columns=TYPE:.type,NAME:.object.metadata.name,UID:.object.metadata.uid,PHASE:.object.status.phase,POD_IP:.object.status.podIP,NODE:.object.spec.nodeName,STATE:.object.status.containerStatuses[0].state,LAST_STATE:.object.status.containerStatuses[0].lastState' \
      2>>"$driver" |
      while IFS= read -r event; do
        read -r event_type pod_name pod_uid _ <<<"$event"
        [[ -n "$event_type" && -n "$pod_name" && -n "$pod_uid" ]] || continue
        printf '%s|%s\n' "$(date -u +%FT%TZ.%N)" "$event" >>"$output_dir/share-manager-pod-events.log"
        if [[ ! -e "$output_dir/seen-${pod_uid}" ]]; then
          : >"$output_dir/seen-${pod_uid}"
          follow_share_manager_logs "$pod_name" "$pod_uid" &
          printf '%s\n' "$!" >>"$child_pids"
        fi
        capture_network_state "$event_type $pod_name uid=$pod_uid"
        if [[ "$event_type" == "DELETED" || "$event" == *terminated* ]]; then
          capture_terminal_pod "$event_type" "$pod_name" "$pod_uid" "$event"
        fi
      done
    printf '%s share-manager pod watch closed; reconnecting\n' "$(date -u +%FT%TZ.%N)" >>"$driver"
    sleep 1
  done
}

cleanup() {
  trap - TERM INT EXIT
  capture_network_state watcher-stop
  while IFS= read -r pid; do
    kill "$pid" >/dev/null 2>&1 || true
  done <"$child_pids"
  for pid in $(jobs -pr); do
    kill "$pid" >/dev/null 2>&1 || true
  done
  wait || true
}
trap cleanup TERM INT EXIT

watch_share_manager_pods &
capture_network_state watcher-start
watch_table nodes "$output_dir/nodes.log" nodes -o 'custom-columns=TYPE:.type,NAME:.object.metadata.name,UID:.object.metadata.uid,POD_CIDRS:.object.spec.podCIDRs,READY:.object.status.conditions[?(@.type=="Ready")].status' &
watch_table agentpools "$output_dir/agentpools.log" agentpools -A -o 'custom-columns=TYPE:.type,NAMESPACE:.object.metadata.namespace,NAME:.object.metadata.name,UID:.object.metadata.uid,READY:.object.status.ready,READY_REPLICAS:.object.status.readyReplicas,CONDITIONS:.object.status.conditions,MESSAGE:.object.status.message' &
watch_table workers "$output_dir/workers.log" pods -A -l platform.iterabase.com/agentpool -o 'custom-columns=TYPE:.type,NAMESPACE:.object.metadata.namespace,NAME:.object.metadata.name,UID:.object.metadata.uid,CREATED:.object.metadata.creationTimestamp,DELETING:.object.metadata.deletionTimestamp,TEMPLATE_HASH:.object.metadata.annotations.platform\\.iterabase\\.com/pod-template-hash,NODE:.object.spec.nodeName,PHASE:.object.status.phase,READY:.object.status.conditions[?(@.type=="Ready")].status,STATE:.object.status.containerStatuses[0].state,LAST_STATE:.object.status.containerStatuses[0].lastState' &
watch_table volumes "$output_dir/volumes.log" volumes.longhorn.io -n longhorn-system -o 'custom-columns=TYPE:.type,NAME:.object.metadata.name,ROBUSTNESS:.object.status.robustness,STATE:.object.status.state,SHARE_STATE:.object.status.shareState,SHARE_ENDPOINT:.object.status.shareEndpoint,CURRENT_NODE:.object.status.currentNodeID,OWNER:.object.status.ownerID' &
watch_table engines "$output_dir/engines.log" engines.longhorn.io -n longhorn-system -o 'custom-columns=TYPE:.type,NAME:.object.metadata.name,VOLUME:.object.spec.volumeName,ACTIVE:.object.spec.active,CURRENT_STATE:.object.status.currentState,INSTANCE_MANAGER:.object.status.instanceManagerName,REPLICA_MODE_MAP:.object.status.replicaModeMap' &
watch_table replicas "$output_dir/replicas.log" replicas.longhorn.io -n longhorn-system -o 'custom-columns=TYPE:.type,NAME:.object.metadata.name,VOLUME:.object.spec.volumeName,NODE:.object.spec.nodeID,DISK:.object.spec.diskID,FAILED_AT:.object.spec.failedAt,HEALTHY_AT:.object.spec.healthyAt,CURRENT_STATE:.object.status.currentState,INSTANCE_MANAGER:.object.status.instanceManagerName' &
watch_table instance-managers "$output_dir/instance-managers.log" instancemanagers.longhorn.io -n longhorn-system -o 'custom-columns=TYPE:.type,NAME:.object.metadata.name,NODE:.object.spec.nodeID,ENGINE_IMAGE:.object.spec.image,STATE:.object.status.currentState,INSTANCES:.object.status.instanceEngines' &
watch_table share-managers "$output_dir/share-managers.log" sharemanagers.longhorn.io -n longhorn-system -o 'custom-columns=TYPE:.type,NAME:.object.metadata.name,STATE:.object.status.state,ENDPOINT:.object.status.endpoint,OWNER:.object.status.ownerID' &
watch_table recovery-endpoints "$output_dir/recovery-endpoints.log" endpointslices.discovery.k8s.io -n longhorn-system -l kubernetes.io/service-name=longhorn-recovery-backend -o 'custom-columns=TYPE:.type,NAME:.object.metadata.name,ADDRESSES:.object.endpoints[*].addresses,READY:.object.endpoints[*].conditions.ready,NODES:.object.endpoints[*].nodeName' &
: >"$output_dir/ready"
wait
`
}

func (watcher *shareManagerAttemptWatcher) stop(t *testing.T) {
	t.Helper()
	if watcher.stopped {
		return
	}
	watcher.stopped = true
	client, err := sshDial(watcher.ip, watcher.keyPath)
	if err != nil {
		t.Logf("retain share-manager attempt watcher %s: dial host: %v", watcher.label, err)
		return
	}
	defer client.Close()
	command := fmt.Sprintf(
		"sudo kill %s >/dev/null 2>&1 || true; for i in $(seq 1 20); do sudo kill -0 %s >/dev/null 2>&1 || break; sleep 0.25; done; sudo bash -c 'for file in \"$1\"/*; do test -f \"$file\" || continue; printf \"===== %%s =====\\n\" \"$file\"; cat \"$file\"; done' _ %s",
		candidateShellQuote(watcher.pid), candidateShellQuote(watcher.pid), candidateShellQuote(watcher.remoteDir),
	)
	output, commandErr := sshOutput(client, command)
	if commandErr != nil {
		t.Logf("retain share-manager attempt watcher %s: %v", watcher.label, commandErr)
	}
	path := filepath.Join(watcher.diagnostics.outputDir, "share-manager-attempts-"+watcher.label+".log")
	if err := os.WriteFile(path, []byte(watcher.diagnostics.redactor.String(output)), 0o600); err != nil {
		t.Logf("write share-manager attempt watcher %s: %v", watcher.label, err)
		return
	}
	t.Logf("retained event-driven share-manager termination/log and Longhorn/network transition evidence at %s", path)
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
		"provisioning":   "cloud-init status --long 2>&1; systemctl --no-pager --full status k3s 2>&1 || true",
		"forge-state":    fmt.Sprintf("sudo ls -la /var/lib/forge/overlay/%s 2>&1 || true; sudo k3s kubectl get gitrepositories,kustomizations -A -o wide 2>&1 || true", state.runID),
		"platform-state": "sudo k3s kubectl get nodes -o wide 2>&1 || true; sudo k3s kubectl get deployments,statefulsets,daemonsets,pods,jobs,pvc -A -o wide 2>&1 || true; sudo k3s kubectl get events -A --sort-by=.metadata.creationTimestamp 2>&1 | tail -300 || true",
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

func TestShareManagerAttemptWatcherRetainsTerminatingEvidence(t *testing.T) {
	t.Parallel()
	script := shareManagerAttemptWatcherScript()
	command := exec.Command("bash", "-n")
	command.Stdin = strings.NewReader(script)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("share-manager watcher script syntax: %v\n%s", err, output)
	}
	for _, contract := range []string{
		"longhorn.io/component=share-manager",
		"--watch --output-watch-events",
		".status.robustness",
		".status.shareState",
		".status.conditions",
		"engines.longhorn.io",
		"replicas.longhorn.io",
		"instancemanagers.longhorn.io",
		"kubernetes.io/service-name=longhorn-recovery-backend",
		"STATE:.object.status.containerStatuses[0].state",
		"LAST_STATE:.object.status.containerStatuses[0].lastState",
		"--follow --timestamps",
		"--- previous share-manager log, if retained ---",
		"--previous --tail=500 --timestamps",
		"iptables-save -c -t filter",
		"ipset save",
	} {
		if !strings.Contains(script, contract) {
			t.Fatalf("share-manager watcher script missing %q:\n%s", contract, script)
		}
	}
	if strings.Contains(script, "sudo k3s") {
		t.Fatalf("root-owned watcher must invoke k3s directly without nested sudo:\n%s", script)
	}
	if strings.Contains(script, "sleep 0.25") {
		t.Fatalf("event-driven watcher must not restore the high-frequency kubectl loop:\n%s", script)
	}
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

func TestBootstrapCredentialInspectionCoversCurrentAndPreviousCollectorLogs(t *testing.T) {
	t.Parallel()
	requests, err := bootstrapLogRequests(`{
		"items": [{
			"metadata": {"name": "api-0"},
			"status": {"initContainerStatuses": [{"name": "bootstrap", "restartCount": 1}]}
		}]
	}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(requests) != 2 || requests[0].previous || !requests[1].previous {
		t.Fatalf("bootstrap log requests = %+v, want current and previous", requests)
	}

	outputs := []string{
		"Admin API key (scope=admin): current-admin-secret\n" +
			"Service account API key (scope=token): current-token-secret\n" +
			strings.Repeat("later current log line\n", shareddiagnostics.PodLogTailLines+50),
		"Admin API key (scope=admin): previous-admin-secret\n" +
			"Service account API key (scope=token): previous-token-secret\n" +
			strings.Repeat("later previous log line\n", shareddiagnostics.PodLogTailLines+50),
	}
	var secrets []string
	for index, request := range requests {
		command := bootstrapCredentialLogCommand(request)
		if !strings.Contains(command, "--tail=-1") {
			t.Fatalf("bootstrap inspection must read the complete log: %s", command)
		}
		if request.previous != strings.Contains(command, "--previous") {
			t.Fatalf("bootstrap previous-log command mismatch: request=%+v command=%s", request, command)
		}
		literals, literalErr := bootstrapSecretLiterals(outputs[index])
		if literalErr != nil {
			t.Fatal(literalErr)
		}
		secrets = append(secrets, literals...)
	}
	if strings.Join(secrets, ",") != "current-admin-secret,current-token-secret,previous-admin-secret,previous-token-secret" {
		t.Fatalf("current/previous bootstrap secrets were not all registered: %v", secrets)
	}
}

func TestBootstrapCredentialInspectionRejectsChangedPreviousFormat(t *testing.T) {
	t.Parallel()
	previous := "Admin bootstrap credential (scope=admin) => previous-admin-secret\n" +
		"Service account API key (scope=token): previous-token-secret\n"
	secrets, err := bootstrapSecretLiterals(previous)
	if err == nil || secrets != nil {
		t.Fatalf("changed previous bootstrap credential format accepted: secrets=%v err=%v", secrets, err)
	}
	if strings.Contains(err.Error(), "previous-admin-secret") {
		t.Fatalf("bootstrap parse error leaked previous credential: %v", err)
	}
}

func TestBootstrapCredentialInspectionOmitsPreviousWithoutRestart(t *testing.T) {
	t.Parallel()
	requests, err := bootstrapLogRequests(`{
		"items": [{
			"metadata": {"name": "api-0"},
			"status": {"initContainerStatuses": [{"name": "bootstrap", "restartCount": 0}]}
		}]
	}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(requests) != 1 || requests[0].previous {
		t.Fatalf("bootstrap log requests = %+v, want current only", requests)
	}
}
