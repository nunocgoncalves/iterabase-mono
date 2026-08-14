//go:build e2e_kind

package kind

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/nunocgoncalves/iterabase-mono/testkit/e2e/process"
	"github.com/nunocgoncalves/iterabase-mono/testkit/e2e/redact"
)

func TestRealKindLifecycleUsesPrivateKubeconfigAndDeletesCluster(t *testing.T) {
	output := t.TempDir()
	runner := process.Runner{Redactor: redact.New(), OutputDir: output}
	cluster, err := (Manager{Executor: runner}).Create(context.Background(), "testkit-lifecycle")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		if err := cluster.Delete(ctx); err != nil {
			t.Errorf("delete cluster: %v", err)
		}
	})
	if _, err := os.Stat(cluster.Kubeconfig); err != nil {
		t.Fatalf("private kubeconfig does not exist: %v", err)
	}
	if _, err := runner.Run(context.Background(), process.Command{
		Name: "kubectl", Args: []string{"--kubeconfig", cluster.Kubeconfig, "get", "nodes"},
		Timeout: 2 * time.Minute, OutputName: "nodes.log",
	}); err != nil {
		t.Fatal(err)
	}
}
