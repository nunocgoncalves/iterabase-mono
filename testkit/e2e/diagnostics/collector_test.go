package diagnostics

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nunocgoncalves/iterabase-mono/testkit/e2e/process"
	"github.com/nunocgoncalves/iterabase-mono/testkit/e2e/redact"
)

type fixtureExecutor struct{ commands []process.Command }

func (executor *fixtureExecutor) Run(_ context.Context, command process.Command) (process.Result, error) {
	executor.commands = append(executor.commands, command)
	joined := strings.Join(command.Args, " ")
	switch {
	case strings.Contains(joined, "get pods -A -o jsonpath="):
		return process.Result{Output: "iterabase-system\tapi-0\n"}, nil
	case command.Name == "helm" && strings.Contains(joined, "list -A -o json"):
		return process.Result{Output: `[{"name":"platform","namespace":"iterabase-system"}]`}, nil
	case command.Name == "helm" && strings.Contains(joined, "get all"):
		return process.Result{Output: `MANIFEST:
---
apiVersion: v1
kind: Secret
metadata:
  name: generated-tls
type: kubernetes.io/tls
data:
  tls.crt: "public-certificate"
  tls.key: "generated-tls-private-key-base64"
---
apiVersion: v1
kind: Secret
metadata:
  name: generated-database
stringData:
  password: "generated-database-password"
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: safe-config
data:
  safe: retained
`}, nil
	case command.Name == "helm" && strings.Contains(joined, "get values"):
		return process.Result{Output: "tls.key: values-private-key\n"}, nil
	default:
		return process.Result{Output: "token: diagnostic-secret\n"}, nil
	}
}

func TestCollectorCapturesRedactedKubernetesHelmAndPodEvidence(t *testing.T) {
	t.Parallel()
	executor := &fixtureExecutor{}
	output := t.TempDir()
	collector := Collector{
		Executor: executor, Kubeconfig: "/tmp/kubeconfig", OutputDir: output,
		Redactor: redact.New("diagnostic-secret"),
	}
	if err := collector.Collect(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{
		"kubernetes-resources.log", "kubernetes-events.log", "describe-iterabase-system-api-0.log",
		"logs-iterabase-system-api-0.log", "helm-get-iterabase-system-platform.log",
		"helm-history-iterabase-system-platform.log", "helm-values-iterabase-system-platform.log",
		"helm-hooks-iterabase-system-platform.log", "helm-status-iterabase-system-platform.log",
	} {
		if _, err := os.Stat(filepath.Join(output, name)); err != nil {
			t.Fatalf("missing diagnostic %s: %v", name, err)
		}
	}
	helmEvidence, err := os.ReadFile(filepath.Join(output, "helm-get-iterabase-system-platform.log"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(helmEvidence), "safe: retained") {
		t.Fatalf("non-Secret Helm evidence was stripped:\n%s", helmEvidence)
	}
	if strings.Count(string(helmEvidence), "<redacted>") < 2 {
		t.Fatalf("generated TLS/password Secret maps were not redacted:\n%s", helmEvidence)
	}
	if err := filepath.Walk(output, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		for _, secret := range []string{
			"diagnostic-secret", "generated-tls-private-key-base64", "generated-database-password", "values-private-key",
		} {
			if strings.Contains(string(data), secret) {
				t.Fatalf("secret %q retained in %s", secret, path)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
