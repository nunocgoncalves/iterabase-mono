package e2e

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nunocgoncalves/iterabase-mono/forge/test/e2e/internal/remotecluster"
	"golang.org/x/crypto/ssh"
)

const workspaceNamespace = "iterabase-system"

var bootstrapCredentialPattern = regexp.MustCompile(`API key \(scope=([^)]+)\): (\S+)`)

type workspaceWorkItem struct {
	ID               string `json:"id"`
	State            string `json:"state"`
	CurrentAttemptID string `json:"currentAttemptId"`
}

type workspaceBlocker struct {
	ID string `json:"id"`
}

type workspaceModelStats struct {
	BarrierArrivals int64 `json:"barrier_arrivals"`
	CapacityWaiting int64 `json:"capacity_waiting"`
}

func refuseProcessHeldWorkspaceDiskStage(t *testing.T, state *digitalOceanCPUState) {
	t.Helper()
	client, err := sshDial(state.ip, state.privKeyPath)
	if err != nil {
		t.Fatalf("ssh dial %s: %v", state.ip, err)
	}
	defer client.Close()
	const pidFile = "/tmp/forge-e2e-workspace-consumer.pid"
	start := fmt.Sprintf(`sudo rm -f %[1]s; sudo nohup bash -c 'exec 9<>"$1"; printf "%%s\n" "$$" > "$2"; exec sleep 300' bash %[2]s %[1]s </dev/null >/tmp/forge-e2e-workspace-consumer.log 2>&1 &
for i in $(seq 1 30); do test -s %[1]s && exit 0; sleep 1; done; exit 1`, pidFile, candidateShellQuote(state.workspaceDevice))
	if output, startErr := sshOutput(client, start); startErr != nil {
		t.Fatalf("start raw-device consumer: %v\n%s", startErr, output)
	}
	stop := func() {
		_, _ = sshOutput(client, fmt.Sprintf(`sudo bash -c '
if test -s %[1]s; then
  pid=$(cat %[1]s)
  kill "$pid" 2>/dev/null || true
  for i in $(seq 1 30); do kill -0 "$pid" 2>/dev/null || break; sleep 0.1; done
  kill -9 "$pid" 2>/dev/null || true
fi
rm -f %[1]s
'`, pidFile))
	}
	defer stop()

	cfg := writeForgeConfigSpec(t, forgeConfigSpec{
		Name: state.runID, Address: state.ip, SSHKeyPath: state.privKeyPath, RunLabel: true, DualStack: true,
	})
	output, applyErr := runForgeE(state.forgeBin, state.forgeHome, "apply", "--config", cfg,
		"--skip-chart", "--skip-gpu", "--skip-overlay", "--skip-secrets", "--skip-flux")
	if applyErr == nil {
		t.Fatalf("Forge formatted a process-held raw workspace disk:\n%s", output)
	}
	if !strings.Contains(output, "held open as a raw block device") {
		t.Fatalf("process-held raw workspace refusal was not actionable:\n%s", output)
	}
	unchanged := mustSSHOutput(t, client, fmt.Sprintf(`sudo bash -ceu '
test ! -e /var/lib/iterabase/agentpool-workspace.receipt
test ! -e /etc/rancher/k3s/k3s.yaml
set +e
signature=$(blkid -p -s TYPE -o value -- %s 2>&1)
rc=$?
set -e
test "$rc" = 2
test -z "$signature"
printf raw-consumer-refusal=pass
'`, state.workspaceDevice))
	if !strings.Contains(unchanged, "raw-consumer-refusal=pass") {
		t.Fatalf("raw-consumer refusal did not preserve the blank pre-install boundary: %s", unchanged)
	}
	stop()
}

func setupWorkspaceExecutionFixtureStage(t *testing.T, state *digitalOceanCPUState) {
	t.Helper()
	repository := os.Getenv("FORGE_E2E_RUNTIME_IMAGE_REPO")
	tag := os.Getenv("FORGE_E2E_RUNTIME_IMAGE_TAG")
	if repository == "" || tag == "" {
		t.Fatal("exact real-machine workspace behavior requires the deterministic runtime fixture image")
	}
	cluster := workspaceCluster(t, state)
	manifest := fmt.Sprintf(`apiVersion: apps/v1
kind: Deployment
metadata: {name: forge-workspace-model, namespace: iterabase-system}
spec:
  replicas: 1
  selector: {matchLabels: {app: forge-workspace-model}}
  template:
    metadata: {labels: {app: forge-workspace-model}}
    spec:
      containers:
        - name: model
          image: %s:%s
          imagePullPolicy: Never
          ports: [{name: http, containerPort: 8080}]
          readinessProbe: {httpGet: {path: /health, port: http}}
          securityContext: {runAsNonRoot: true, allowPrivilegeEscalation: false, capabilities: {drop: [ALL]}}
---
apiVersion: v1
kind: Service
metadata: {name: forge-workspace-model, namespace: iterabase-system}
spec:
  selector: {app: forge-workspace-model}
  ports: [{name: http, port: 8080, targetPort: http}]
---
apiVersion: platform.iterabase.com/v1alpha1
kind: ModelBackend
metadata: {name: forge-workspace-model, namespace: iterabase-system}
spec:
  kind: external
  external: {baseURL: http://forge-workspace-model.iterabase-system.svc:8080}
---
apiVersion: platform.iterabase.com/v1alpha1
kind: Model
metadata: {name: forge-workspace-model, namespace: iterabase-system}
spec:
  modelID: forge-workspace-model
  displayName: Forge workspace deterministic model
  contextLength: 8192
  capabilities: [chat, tools]
  backendRef: forge-workspace-model
  defaultParams: {max_tokens: 1024}
  transforms: {rewrite_model_name: true}
---
%s
---
%s
---
%s
---
%s
`, repository, tag,
		workspaceAgentWorkflow("forge-workspace-a", "e2e/forge-workspace-a", workspaceBarrierPrompt("session-a")),
		workspaceAgentWorkflow("forge-workspace-b", "e2e/forge-workspace-b", workspaceBarrierPrompt("session-b")),
		workspaceAgentWorkflow("forge-workspace-capacity", "e2e/forge-workspace-capacity", workspaceCapacityPrompt()),
		workspaceHumanGateWorkflow())
	applyWorkspaceManifest(t, cluster, "workspace-real-behavior.yaml", manifest)
	cluster.Kubectl(t, "rollout", "status", "deployment/forge-workspace-model", "-n", workspaceNamespace, "--timeout=3m")
	cluster.Kubectl(t, "wait", "--for=jsonpath={.status.deployed}=true", "modelbackend/forge-workspace-model", "-n", workspaceNamespace, "--timeout=3m")
	cluster.Kubectl(t, "wait", "--for=jsonpath={.status.available}=true", "model/forge-workspace-model", "-n", workspaceNamespace, "--timeout=3m")
	for _, workflow := range []string{"forge-workspace-a", "forge-workspace-b", "forge-workspace-capacity", "forge-workspace-recovery"} {
		cluster.Kubectl(t, "wait", "--for=jsonpath={.status.ready}=true", "workflow/"+workflow, "-n", workspaceNamespace, "--timeout=3m")
	}

	apiPod := cluster.FirstPodName(t, workspaceNamespace, "app.kubernetes.io/name=control-plane,app.kubernetes.io/component=api")
	bootstrap := cluster.PodLogs(t, workspaceNamespace, apiPod, "bootstrap")
	keys := map[string]string{}
	for _, match := range bootstrapCredentialPattern.FindAllStringSubmatch(bootstrap, -1) {
		keys[match[1]] = match[2]
	}
	if keys["admin"] == "" {
		t.Fatal("exact candidate bootstrap did not emit an admin credential")
	}
	state.workspaceAdminKey = keys["admin"]
	state.diagnostics.redactor.Add(state.workspaceAdminKey)

	baseURL, stop := openWorkspaceAPI(t, cluster, state)
	defer stop()
	status, body := workspaceAPIRequest(t, baseURL, state.workspaceAdminKey, http.MethodPost, "/v1/users", map[string]any{
		"email": "forge-workspace-e2e@example.test", "role": "user",
	}, nil)
	requireWorkspaceStatus(t, status, http.StatusCreated)
	var user struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(body, &user); err != nil || user.ID == "" {
		t.Fatalf("decode workspace E2E identity: %v", err)
	}
	status, body = workspaceAPIRequest(t, baseURL, state.workspaceAdminKey, http.MethodPost, "/v1/api-keys", map[string]any{
		"identityID": user.ID, "name": "forge-workspace-e2e", "scope": "work",
	}, nil)
	requireWorkspaceStatus(t, status, http.StatusCreated)
	var key struct {
		FullKey string `json:"fullKey"`
	}
	if err := json.Unmarshal(body, &key); err != nil || key.FullKey == "" {
		t.Fatalf("decode workspace E2E work credential: %v", err)
	}
	state.workspaceWorkKey = key.FullKey
	state.diagnostics.redactor.Add(state.workspaceWorkKey)
}

func exerciseConcurrentWorkspaceWorkStage(t *testing.T, state *digitalOceanCPUState) {
	t.Helper()
	cluster := workspaceCluster(t, state)
	baseURL, stopAPI := openWorkspaceAPI(t, cluster, state)
	defer stopAPI()
	modelURL, stopModel := openWorkspaceService(t, cluster, "svc/forge-workspace-model", 8080)
	defer stopModel()

	first := startWorkspaceWork(t, baseURL, state.workspaceWorkKey, "e2e/forge-workspace-a", "Concurrent workspace session A", "hor-538-workspace-a")
	second := startWorkspaceWork(t, baseURL, state.workspaceWorkKey, "e2e/forge-workspace-b", "Concurrent workspace session B", "hor-538-workspace-b")
	waitWorkspaceModelStats(t, modelURL, 2, -1, 2*time.Minute)
	first = waitWorkspaceWorkState(t, baseURL, state.workspaceWorkKey, first.ID, "done", 4*time.Minute)
	second = waitWorkspaceWorkState(t, baseURL, state.workspaceWorkKey, second.ID, "done", 4*time.Minute)

	assignments := workspaceDatabaseQuery(t, cluster, state, fmt.Sprintf(`SELECT count(DISTINCT worker_id)::text || '|' || count(*)::text FROM runtime.turn_assignments WHERE attempt_id IN ('%s','%s')`, first.CurrentAttemptID, second.CurrentAttemptID))
	if assignments != "2|2" {
		t.Fatalf("real-machine synchronization barrier did not consume two simultaneous worker credits: %q", assignments)
	}
	sessions := workspaceDatabaseQuery(t, cluster, state, fmt.Sprintf(`SELECT count(DISTINCT session_id) FROM runtime.workflow_runs WHERE id IN ('%s','%s')`, first.CurrentAttemptID, second.CurrentAttemptID))
	if sessions != "2" {
		t.Fatalf("concurrent work did not retain two isolated sessions: %q", sessions)
	}
	for _, evidence := range []string{workspaceMarkerDigest("session-a"), workspaceMarkerDigest("session-b")} {
		count := workspaceDatabaseQuery(t, cluster, state, fmt.Sprintf(`SELECT count(*) FROM runtime.events WHERE run_id IN ('%s','%s') AND kind='tool_result' AND payload::text LIKE '%%%s%%'`, first.CurrentAttemptID, second.CurrentAttemptID, evidence))
		if count != "1" {
			t.Fatalf("checksummed isolated marker evidence %s count=%s, want 1", evidence, count)
		}
	}
}

func exerciseActiveWorkspaceCapacityStage(t *testing.T, state *digitalOceanCPUState) {
	t.Helper()
	cluster := workspaceCluster(t, state)
	baseURL, stopAPI := openWorkspaceAPI(t, cluster, state)
	defer stopAPI()
	modelURL, stopModel := openWorkspaceService(t, cluster, "svc/forge-workspace-model", 8080)
	defer stopModel()
	client, err := sshDial(state.ip, state.privKeyPath)
	if err != nil {
		t.Fatalf("ssh dial %s: %v", state.ip, err)
	}
	defer client.Close()
	const filler = "/var/lib/iterabase/agentpool-workspaces/.forge-e2e-capacity-fill"
	removeFiller := func() { _, _ = sshOutput(client, "sudo rm -f "+filler+" && sync") }
	defer removeFiller()

	active := startWorkspaceWork(t, baseURL, state.workspaceWorkKey, "e2e/forge-workspace-capacity", "Active turn across capacity floor", "hor-538-capacity-active")
	waitWorkspaceModelStats(t, modelURL, -1, 1, 2*time.Minute)
	waitWorkspaceDatabaseValue(t, cluster, state, fmt.Sprintf(`SELECT count(*) FROM runtime.turn_assignments WHERE attempt_id='%s' AND state='active'`, active.CurrentAttemptID), "1", time.Minute)
	setWorkspaceFreeTarget(t, client, filler, 19)
	waitWorkspaceGate(t, client, true, storageReasonWorkspaceCapacityGatedE2E, 3*time.Minute)

	// The model request remains deliberately blocked while the actual filesystem
	// crosses the floor. The threshold alone must not terminalize/fence it.
	time.Sleep(12 * time.Second)
	active = readWorkspaceWork(t, baseURL, state.workspaceWorkKey, active.ID)
	if active.State != "in_progress" {
		t.Fatalf("capacity threshold aborted the active work item: state=%s", active.State)
	}
	if got := workspaceDatabaseQuery(t, cluster, state, fmt.Sprintf(`SELECT count(*) FROM runtime.turn_assignments WHERE attempt_id='%s' AND state='active'`, active.CurrentAttemptID)); got != "1" {
		t.Fatalf("capacity threshold fenced the active assignment: active=%s", got)
	}

	status, _ := workspaceAPIRequest(t, modelURL, "", http.MethodPost, "/release/capacity", nil, nil)
	requireWorkspaceStatus(t, status, http.StatusNoContent)
	active = waitWorkspaceWorkState(t, baseURL, state.workspaceWorkKey, active.ID, "done", 4*time.Minute)
	queued := startWorkspaceWork(t, baseURL, state.workspaceWorkKey, "e2e/forge-workspace-capacity", "Queued while workspace gated", "hor-538-capacity-queued")
	time.Sleep(15 * time.Second)
	if got := workspaceDatabaseQuery(t, cluster, state, fmt.Sprintf(`SELECT count(*) FROM runtime.turn_assignments WHERE attempt_id='%s'`, queued.CurrentAttemptID)); got != "0" {
		t.Fatalf("fresh assignment escaped the <=20%% capacity gate: assignments=%s", got)
	}

	// Return only to 22% and replace a worker. The shared/durable gate must stay
	// closed in the hysteresis band on the fresh supervisor.
	setWorkspaceFreeTarget(t, client, filler, 22)
	waitWorkspaceGate(t, client, true, storageReasonWorkspaceCapacityGatedE2E, 2*time.Minute)
	replaceIdleWorkspaceWorkerAtGate(t, cluster, state)
	waitWorkspaceGate(t, client, true, storageReasonWorkspaceCapacityGatedE2E, 2*time.Minute)
	if got := workspaceDatabaseQuery(t, cluster, state, fmt.Sprintf(`SELECT count(*) FROM runtime.turn_assignments WHERE attempt_id='%s'`, queued.CurrentAttemptID)); got != "0" {
		t.Fatalf("replacement worker reopened credit inside the 20-25%% band: assignments=%s", got)
	}

	removeFiller()
	waitWorkspaceGate(t, client, false, storageReasonWorkspaceCapacityHealthyE2E, 3*time.Minute)
	_ = waitWorkspaceWorkState(t, baseURL, state.workspaceWorkKey, queued.ID, "done", 4*time.Minute)
}

func exerciseHumanGateWorkspaceReplacementStage(t *testing.T, state *digitalOceanCPUState) {
	t.Helper()
	cluster := workspaceCluster(t, state)
	cluster.Kubectl(t, "patch", "agentpool/forge-storage-pool", "-n", workspaceNamespace, "--type=merge", "-p", `{"spec":{"replicas":1}}`)
	waitWorkspacePoolReplicas(t, cluster, 1, 4*time.Minute)
	baseURL, stopAPI := openWorkspaceAPI(t, cluster, state)
	defer stopAPI()

	item := startWorkspaceWork(t, baseURL, state.workspaceWorkKey, "e2e/forge-workspace-recovery", "Human-gated workspace recovery", "hor-538-human-recovery")
	item = waitWorkspaceWorkState(t, baseURL, state.workspaceWorkKey, item.ID, "blocked", 4*time.Minute)
	sessionBefore := workspaceDatabaseQuery(t, cluster, state, fmt.Sprintf(`SELECT session_id FROM runtime.workflow_runs WHERE id='%s'`, item.CurrentAttemptID))
	assignedWorker := workspaceDatabaseQuery(t, cluster, state, fmt.Sprintf(`SELECT worker_id FROM runtime.turn_assignments WHERE attempt_id='%s' ORDER BY assigned_at LIMIT 1`, item.CurrentAttemptID))
	if assignedWorker == "" {
		t.Fatal("human-gated work has no attributable initial worker")
	}
	oldUID := strings.TrimSpace(cluster.Kubectl(t, "get", "pod/"+assignedWorker, "-n", workspaceNamespace, "-o", "jsonpath={.metadata.uid}"))
	pvcBefore := strings.TrimSpace(cluster.Kubectl(t, "get", "pvc/forge-storage-pool-sandbox", "-n", workspaceNamespace, "-o", "jsonpath={.metadata.uid}"))
	cluster.Kubectl(t, "delete", "pod/"+assignedWorker, "-n", workspaceNamespace, "--wait=true", "--timeout=3m")
	waitWorkspaceReplacementPod(t, cluster, assignedWorker, oldUID, 4*time.Minute)
	pvcAfter := strings.TrimSpace(cluster.Kubectl(t, "get", "pvc/forge-storage-pool-sandbox", "-n", workspaceNamespace, "-o", "jsonpath={.metadata.uid}"))
	if pvcAfter != pvcBefore {
		t.Fatalf("human-gate worker replacement changed the RWO claim: before=%s after=%s", pvcBefore, pvcAfter)
	}

	status, body := workspaceAPIRequest(t, baseURL, state.workspaceWorkKey, http.MethodGet, "/v1/work-items/"+item.ID+"/blocker", nil, nil)
	requireWorkspaceStatus(t, status, http.StatusOK)
	var blocker workspaceBlocker
	if err := json.Unmarshal(body, &blocker); err != nil || blocker.ID == "" {
		t.Fatalf("decode human-gate blocker: %v", err)
	}
	status, _ = workspaceAPIRequest(t, baseURL, state.workspaceWorkKey, http.MethodPost, "/v1/work-blockers/"+blocker.ID+"/responses", map[string]any{
		"outcome": "continued", "response": map[string]any{},
	}, nil)
	requireWorkspaceStatus(t, status, http.StatusOK)
	item = waitWorkspaceWorkState(t, baseURL, state.workspaceWorkKey, item.ID, "done", 4*time.Minute)
	sessionAfter := workspaceDatabaseQuery(t, cluster, state, fmt.Sprintf(`SELECT session_id FROM runtime.workflow_runs WHERE id='%s'`, item.CurrentAttemptID))
	if sessionAfter != sessionBefore {
		t.Fatalf("human-gate resume changed durable session identity: before=%s after=%s", sessionBefore, sessionAfter)
	}
	assignments := workspaceDatabaseQuery(t, cluster, state, fmt.Sprintf(`SELECT count(*)::text || '|' || count(DISTINCT fencing_generation)::text FROM runtime.turn_assignments WHERE attempt_id='%s'`, item.CurrentAttemptID))
	if assignments != "2|2" {
		t.Fatalf("human-gate resume did not use a fresh fenced worker generation: %q", assignments)
	}
	bashCalls := workspaceDatabaseQuery(t, cluster, state, fmt.Sprintf(`SELECT count(*) FROM runtime.events WHERE run_id='%s' AND kind='tool_call_started' AND payload->>'tool_name'='bash'`, item.CurrentAttemptID))
	if bashCalls != "2" {
		t.Fatalf("worker replacement duplicated or omitted the intended workspace consequence: bash calls=%s", bashCalls)
	}
	for _, evidence := range []string{"recovery-initial=" + workspaceMarkerDigest("recovery-marker"), "recovery-resume=" + workspaceMarkerDigest("recovery-marker")} {
		count := workspaceDatabaseQuery(t, cluster, state, fmt.Sprintf(`SELECT count(*) FROM runtime.events WHERE run_id='%s' AND kind='tool_result' AND payload::text LIKE '%%%s%%'`, item.CurrentAttemptID, evidence))
		if count != "1" {
			t.Fatalf("human-gate durable marker evidence %q count=%s, want 1", evidence, count)
		}
	}
}

const (
	storageReasonWorkspaceCapacityGatedE2E   = "WorkspaceCapacityGateActive"
	storageReasonWorkspaceCapacityHealthyE2E = "WorkspaceCapacityHealthy"
)

func workspaceCluster(t *testing.T, state *digitalOceanCPUState) *remotecluster.Cluster {
	t.Helper()
	return remotecluster.Use(t, filepath.Join(state.forgeHome, state.runID, "kubeconfig.yaml"))
}

func applyWorkspaceManifest(t *testing.T, cluster *remotecluster.Cluster, name, manifest string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(manifest), 0o600); err != nil {
		t.Fatalf("write workspace behavior manifest: %v", err)
	}
	cluster.Kubectl(t, "apply", "-f", path)
}

func openWorkspaceAPI(t *testing.T, cluster *remotecluster.Cluster, state *digitalOceanCPUState) (string, func()) {
	t.Helper()
	return openWorkspaceService(t, cluster, "svc/"+state.runID+"-control-plane-api", 8080)
}

func openWorkspaceService(t *testing.T, cluster *remotecluster.Cluster, service string, port int) (string, func()) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve local port: %v", err)
	}
	localPort := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	return cluster.PortForward(t, workspaceNamespace, service, port, localPort)
}

func workspaceAPIRequest(t *testing.T, baseURL, key, method, path string, body any, headers map[string]string) (int, []byte) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal workspace API request: %v", err)
		}
		reader = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(context.Background(), method, baseURL+path, reader)
	if err != nil {
		t.Fatalf("build workspace API request: %v", err)
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if key != "" {
		request.Header.Set("Authorization", "Bearer "+key)
	}
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	response, err := (&http.Client{Timeout: 15 * time.Second}).Do(request)
	if err != nil {
		t.Fatalf("workspace API %s %s: %v", method, path, err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		t.Fatalf("read workspace API response: %v", err)
	}
	return response.StatusCode, data
}

func requireWorkspaceStatus(t *testing.T, got int, want ...int) {
	t.Helper()
	for _, candidate := range want {
		if got == candidate {
			return
		}
	}
	t.Fatalf("workspace API status=%d want=%v", got, want)
}

func startWorkspaceWork(t *testing.T, baseURL, key, workflowKey, title, idempotencyKey string) workspaceWorkItem {
	t.Helper()
	payload := map[string]any{
		"workflowKey": workflowKey,
		"title":       title,
		"source":      map[string]any{"fixture": "HOR-538"},
		"sourcePresentation": map[string]any{
			"kind": "api", "title": title, "subtitle": "Exact-candidate workspace behavior",
		},
	}
	headers := map[string]string{"Idempotency-Key": idempotencyKey}
	status, body := workspaceAPIRequest(t, baseURL, key, http.MethodPost, "/v1/work-items", payload, headers)
	requireWorkspaceStatus(t, status, http.StatusCreated)
	var item workspaceWorkItem
	if err := json.Unmarshal(body, &item); err != nil || item.ID == "" || item.CurrentAttemptID == "" {
		t.Fatalf("decode started workspace work: %v", err)
	}
	status, replayBody := workspaceAPIRequest(t, baseURL, key, http.MethodPost, "/v1/work-items", payload, headers)
	requireWorkspaceStatus(t, status, http.StatusOK, http.StatusCreated)
	var replay workspaceWorkItem
	if err := json.Unmarshal(replayBody, &replay); err != nil || replay.ID != item.ID || replay.CurrentAttemptID != item.CurrentAttemptID {
		t.Fatalf("idempotent manual_api replay diverged: first=%s/%s replay=%s/%s err=%v", item.ID, item.CurrentAttemptID, replay.ID, replay.CurrentAttemptID, err)
	}
	return item
}

func readWorkspaceWork(t *testing.T, baseURL, key, id string) workspaceWorkItem {
	t.Helper()
	status, body := workspaceAPIRequest(t, baseURL, key, http.MethodGet, "/v1/work-items/"+id, nil, nil)
	requireWorkspaceStatus(t, status, http.StatusOK)
	var item workspaceWorkItem
	if err := json.Unmarshal(body, &item); err != nil {
		t.Fatalf("decode workspace work item: %v", err)
	}
	return item
}

func waitWorkspaceWorkState(t *testing.T, baseURL, key, id, wanted string, timeout time.Duration) workspaceWorkItem {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var item workspaceWorkItem
	for time.Now().Before(deadline) {
		item = readWorkspaceWork(t, baseURL, key, id)
		if item.State == wanted {
			return item
		}
		if item.State == "failed" && wanted != "failed" {
			t.Fatalf("workspace work %s failed while waiting for %s", id, wanted)
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("workspace work %s state=%s did not reach %s", id, item.State, wanted)
	return workspaceWorkItem{}
}

func workspaceDatabaseQuery(t *testing.T, cluster *remotecluster.Cluster, state *digitalOceanCPUState, query string) string {
	t.Helper()
	return strings.TrimSpace(cluster.Kubectl(t, "exec", "-n", workspaceNamespace,
		"statefulset/"+state.runID+"-postgresql", "-c", "postgresql", "--",
		"psql", "-U", "controlplane", "-d", "controlplane", "-Atc", query))
}

func waitWorkspaceDatabaseValue(t *testing.T, cluster *remotecluster.Cluster, state *digitalOceanCPUState, query, wanted string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	last := ""
	for time.Now().Before(deadline) {
		last = workspaceDatabaseQuery(t, cluster, state, query)
		if last == wanted {
			return
		}
		time.Sleep(time.Second)
	}
	t.Fatalf("database query did not reach %q (last=%q): %s", wanted, last, query)
}

func waitWorkspaceModelStats(t *testing.T, baseURL string, barrierArrivals, capacityWaiting int64, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last workspaceModelStats
	for time.Now().Before(deadline) {
		status, body := workspaceAPIRequest(t, baseURL, "", http.MethodGet, "/stats", nil, nil)
		if status == http.StatusOK && json.Unmarshal(body, &last) == nil &&
			(barrierArrivals < 0 || last.BarrierArrivals >= barrierArrivals) &&
			(capacityWaiting < 0 || last.CapacityWaiting >= capacityWaiting) {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("deterministic model stats did not reach barrier=%d capacity=%d (last=%+v)", barrierArrivals, capacityWaiting, last)
}

func setWorkspaceFreeTarget(t *testing.T, client *ssh.Client, filler string, targetPercent int) {
	t.Helper()
	command := fmt.Sprintf(`sudo bash -ceu '
mount=/var/lib/iterabase/agentpool-workspaces
current=0
test ! -e %s || current=$(stat -c %%s %s)
read -r size avail < <(df -B1 --output=size,avail "$mount" | tail -1)
target=$((size * %d / 100))
new_size=$((current + avail - target))
test "$new_size" -gt 0
if test "$new_size" -ge "$current"; then
  fallocate -l "$new_size" %s
else
  truncate -s "$new_size" %s
fi
sync -f %s
read -r final_size final_avail < <(df -B1 --output=size,avail "$mount" | tail -1)
percent=$((final_avail * 100 / final_size))
test "$percent" -ge $((%d - 1))
test "$percent" -le $((%d + 1))
printf "workspace-free-target=%%s actual=%%s\n" %d "$percent"
'`, filler, filler, targetPercent, filler, filler, filler, targetPercent, targetPercent, targetPercent)
	output, err := sshOutput(client, command)
	if err != nil {
		t.Fatalf("set workspace filesystem to %d%% free: %v\n%s", targetPercent, err, output)
	}
}

func waitWorkspaceGate(t *testing.T, client *ssh.Client, gated bool, reason string, timeout time.Duration) {
	t.Helper()
	want := "0"
	if gated {
		want = "1"
	}
	deadline := time.Now().Add(timeout)
	last := ""
	for time.Now().Before(deadline) {
		command := fmt.Sprintf(`set -eu
ok=0
total=0
for pod in $(sudo k3s kubectl get pods -n iterabase-system -l platform.iterabase.com/agentpool=forge-storage-pool -o name | cut -d/ -f2); do
  total=$((total+1))
  metrics=$(sudo k3s kubectl get --raw "/api/v1/namespaces/iterabase-system/pods/$pod:8081/proxy/metrics" 2>/dev/null || true)
  if printf '%%s\n' "$metrics" | grep -Eq '^control_plane_harness_workspace_credit_gated %s(\.0+)?$'; then ok=$((ok+1)); fi
done
condition=$(sudo k3s kubectl get agentpool forge-storage-pool -n iterabase-system -o jsonpath='{.status.conditions[?(@.type=="WorkspaceCapacityHealthy")].reason}')
printf '%%s|%%s|%%s\n' "$total" "$ok" "$condition"`, want)
		output, err := sshOutput(client, command)
		last = strings.TrimSpace(output)
		if err == nil {
			parts := strings.Split(last, "|")
			if len(parts) == 3 && parts[0] != "0" && parts[0] == parts[1] && parts[2] == reason {
				return
			}
		}
		time.Sleep(2 * time.Second)
	}
	t.Fatalf("workspace gate did not reach gated=%t condition=%s (last=%q)", gated, reason, last)
}

func replaceIdleWorkspaceWorkerAtGate(t *testing.T, cluster *remotecluster.Cluster, state *digitalOceanCPUState) {
	t.Helper()
	pod := strings.Fields(cluster.Kubectl(t, "get", "pods", "-n", workspaceNamespace, "-l", "platform.iterabase.com/agentpool=forge-storage-pool", "-o", "name"))[0]
	name := strings.TrimPrefix(pod, "pod/")
	oldUID := strings.TrimSpace(cluster.Kubectl(t, "get", pod, "-n", workspaceNamespace, "-o", "jsonpath={.metadata.uid}"))
	pvcBefore := strings.TrimSpace(cluster.Kubectl(t, "get", "pvc/forge-storage-pool-sandbox", "-n", workspaceNamespace, "-o", "jsonpath={.metadata.uid}"))
	cluster.Kubectl(t, "delete", pod, "-n", workspaceNamespace, "--wait=true", "--timeout=3m")
	waitWorkspaceReplacementPod(t, cluster, name, oldUID, 4*time.Minute)
	pvcAfter := strings.TrimSpace(cluster.Kubectl(t, "get", "pvc/forge-storage-pool-sandbox", "-n", workspaceNamespace, "-o", "jsonpath={.metadata.uid}"))
	if pvcAfter != pvcBefore || pvcAfter != state.agentPoolPVCUID {
		t.Fatalf("in-band worker replacement changed AgentPool PVC: original=%s before=%s after=%s", state.agentPoolPVCUID, pvcBefore, pvcAfter)
	}
}

func waitWorkspaceReplacementPod(t *testing.T, cluster *remotecluster.Cluster, name, oldUID string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	last := ""
	for time.Now().Before(deadline) {
		last = strings.TrimSpace(cluster.Kubectl(t, "get", "pod/"+name, "-n", workspaceNamespace, "--ignore-not-found", "-o", `jsonpath={.metadata.uid}|{.status.conditions[?(@.type=="Ready")].status}`))
		parts := strings.Split(last, "|")
		if len(parts) == 2 && parts[0] != "" && parts[0] != oldUID && parts[1] == "True" {
			return
		}
		time.Sleep(2 * time.Second)
	}
	t.Fatalf("worker %s was not replaced Ready (old=%s last=%q)", name, oldUID, last)
}

func waitWorkspacePoolReplicas(t *testing.T, cluster *remotecluster.Cluster, replicas int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	last := ""
	for time.Now().Before(deadline) {
		last = strings.TrimSpace(cluster.Kubectl(t, "get", "agentpool/forge-storage-pool", "-n", workspaceNamespace,
			"-o", `jsonpath={.status.ready}|{.status.readyReplicas}`))
		pods := strings.Fields(cluster.Kubectl(t, "get", "pods", "-n", workspaceNamespace, "-l", "platform.iterabase.com/agentpool=forge-storage-pool", "-o", "name"))
		if last == "true|"+strconv.Itoa(replicas) && len(pods) == replicas {
			return
		}
		time.Sleep(2 * time.Second)
	}
	t.Fatalf("AgentPool did not settle at %d replicas (last=%q)", replicas, last)
}

func workspaceAgentWorkflow(name, key, prompt string) string {
	return fmt.Sprintf(`apiVersion: platform.iterabase.com/v1alpha1
kind: Workflow
metadata: {name: %s, namespace: iterabase-system}
spec:
  key: %s
  version: "1"
  poolRef: forge-storage-pool
  defaultModelRef: forge-workspace-model
  source: {type: manual_api}
  graph:
    entryNode: execute
    maxTransitions: 4
    nodes:
      - key: execute
        label: {en: Execute workspace proof, pt: Executar prova do workspace}
        kind: agent_task
        prompt: %q
        workspaceTools: true
        outcomes: [completed]
        outputSchema: {type: object, additionalProperties: false, required: [result], properties: {result: {type: string}}}
        resultPresentation:
          outcomes: [{outcome: completed, summary: {en: Workspace proof completed, pt: Prova do workspace concluída}}]
          fields: [{path: [result], label: {en: Result, pt: Resultado}}]
    terminalOutcomes: [{node: execute, outcome: completed}]
  presentation: {workflowTitle: Workspace behavior proof, personaName: E2E Operator, locale: en}
`, name, key, prompt)
}

func workspaceHumanGateWorkflow() string {
	initial := recoveryInitialCommand()
	resume := recoveryResumeCommand()
	return fmt.Sprintf(`apiVersion: platform.iterabase.com/v1alpha1
kind: Workflow
metadata: {name: forge-workspace-recovery, namespace: iterabase-system}
spec:
  key: e2e/forge-workspace-recovery
  version: "1"
  poolRef: forge-storage-pool
  defaultModelRef: forge-workspace-model
  source: {type: manual_api}
  graph:
    entryNode: execute
    maxTransitions: 8
    nodes:
      - key: execute
        label: {en: Write durable workspace state, pt: Escrever estado durável}
        kind: agent_task
        prompt: %q
        workspaceTools: true
        outcomes: [completed]
      - key: hold
        label: {en: Await customer response, pt: Aguardar resposta}
        kind: human_gate
        outcomes: [continued]
        humanGate:
          type: approval
          title: {en: Continue workspace recovery, pt: Continuar recuperação}
          description: {en: Resume the same durable session., pt: Retomar a mesma sessão durável.}
          responseSchema: {type: object, additionalProperties: false, properties: {}}
          presentation: {outcomes: [{en: Continued, pt: Continuado}], fields: []}
      - key: resume
        label: {en: Verify durable workspace state, pt: Verificar estado durável}
        kind: agent_task
        prompt: %q
        workspaceTools: true
        outcomes: [completed]
        outputSchema: {type: object, additionalProperties: false, required: [result], properties: {result: {type: string}}}
        resultPresentation:
          outcomes: [{outcome: completed, summary: {en: Recovery completed, pt: Recuperação concluída}}]
          fields: [{path: [result], label: {en: Result, pt: Resultado}}]
    edges:
      - {from: execute, outcome: completed, to: hold}
      - {from: hold, outcome: continued, to: resume}
    terminalOutcomes: [{node: resume, outcome: completed}]
  presentation: {workflowTitle: Workspace replacement recovery, personaName: E2E Operator, locale: en}
`, "E2E_MODE:isolation E2E_BASH:"+base64.StdEncoding.EncodeToString([]byte(initial)), "E2E_MODE:isolation E2E_BASH:"+base64.StdEncoding.EncodeToString([]byte(resume)))
}

func workspaceBarrierPrompt(marker string) string {
	command := fmt.Sprintf(`set -eu
printf %s > marker.txt
test "$(sha256sum marker.txt | cut -d" " -f1)" = %s
if ls /data/sandboxes >/dev/null 2>&1; then exit 90; fi
if touch /data/sandboxes/forbidden-%s >/dev/null 2>&1; then exit 91; fi
printf 'workspace-marker=%s'`, marker, workspaceMarkerDigest(marker), marker, workspaceMarkerDigest(marker))
	return "E2E_MODE:workspace-barrier E2E_BASH:" + base64.StdEncoding.EncodeToString([]byte(command))
}

func workspaceCapacityPrompt() string {
	command := fmt.Sprintf(`set -eu
printf capacity-active > capacity-marker.txt
test "$(sha256sum capacity-marker.txt | cut -d" " -f1)" = %s
printf 'capacity-marker=%s'`, workspaceMarkerDigest("capacity-active"), workspaceMarkerDigest("capacity-active"))
	return "E2E_MODE:capacity-active E2E_BASH:" + base64.StdEncoding.EncodeToString([]byte(command))
}

func recoveryInitialCommand() string {
	return fmt.Sprintf(`set -eu
test ! -e consequence.count
printf once > consequence.count
printf recovery-marker > recovery-marker.txt
test "$(sha256sum recovery-marker.txt | cut -d" " -f1)" = %s
printf 'recovery-initial=%s'`, workspaceMarkerDigest("recovery-marker"), workspaceMarkerDigest("recovery-marker"))
}

func recoveryResumeCommand() string {
	return fmt.Sprintf(`set -eu
test "$(cat consequence.count)" = once
test "$(cat recovery-marker.txt)" = recovery-marker
test "$(sha256sum recovery-marker.txt | cut -d" " -f1)" = %s
printf 'recovery-resume=%s'`, workspaceMarkerDigest("recovery-marker"), workspaceMarkerDigest("recovery-marker"))
}

func workspaceMarkerDigest(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}
