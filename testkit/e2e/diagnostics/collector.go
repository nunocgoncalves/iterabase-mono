// Package diagnostics collects redacted Kubernetes, Helm, process, and component evidence.
package diagnostics

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nunocgoncalves/iterabase-mono/testkit/e2e/artifacts"
	"github.com/nunocgoncalves/iterabase-mono/testkit/e2e/process"
	"github.com/nunocgoncalves/iterabase-mono/testkit/e2e/redact"
)

// Collector gathers actionable failure evidence without querying Kubernetes Secrets.
type Collector struct {
	Executor   process.Executor
	Kubeconfig string
	OutputDir  string
	Redactor   *redact.Redactor
	Artifacts  []artifacts.Entry
}

// Collect is best-effort across evidence sources and returns all collection errors.
func (collector Collector) Collect(ctx context.Context) error {
	if collector.Executor == nil || collector.Kubeconfig == "" || collector.OutputDir == "" {
		return fmt.Errorf("diagnostics require executor, kubeconfig, and output directory")
	}
	if err := os.MkdirAll(collector.OutputDir, 0o700); err != nil {
		return err
	}
	var collected []error
	run := func(name, command string, args ...string) string {
		result, err := collector.Executor.Run(ctx, process.Command{Name: command, Args: args, Timeout: 2 * time.Minute})
		output := collector.Redactor.String(result.Output)
		if writeErr := os.WriteFile(filepath.Join(collector.OutputDir, name+".log"), []byte(output), 0o600); writeErr != nil {
			collected = append(collected, writeErr)
		}
		if err != nil {
			collected = append(collected, fmt.Errorf("%s: %w", name, err))
		}
		return output
	}
	kube := func(name string, args ...string) string {
		return run(name, "kubectl", append([]string{"--kubeconfig", collector.Kubeconfig}, args...)...)
	}
	helm := func(name string, args ...string) string {
		return run(name, "helm", append([]string{"--kubeconfig", collector.Kubeconfig}, args...)...)
	}

	kube("kubernetes-resources", "get",
		"namespaces,nodes,pods,deployments,statefulsets,daemonsets,jobs,services,endpointslices,ingresses,persistentvolumeclaims",
		"-A", "-o", "yaml")
	kube("kubernetes-events", "get", "events", "-A", "--sort-by=.lastTimestamp", "-o", "yaml")
	// Parse only namespace/name pairs. Redacting a full Pod JSON document before
	// parsing can intentionally rewrite credential-shaped fields inside pod specs
	// and make otherwise valid JSON malformed. Names are sufficient to drive the
	// subsequent describe/log collection and cannot contain secret values.
	podNames := kube("kubernetes-pods", "get", "pods", "-A", "-o", `jsonpath={range .items[*]}{.metadata.namespace}{"\t"}{.metadata.name}{"\n"}{end}`)
	helmJSON := helm("helm-list", "list", "-A", "-o", "json")

	for _, line := range strings.Split(strings.TrimSpace(podNames), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if len(fields) != 2 {
			collected = append(collected, fmt.Errorf("decode pod identity %q", line))
			continue
		}
		namespace, name := fields[0], fields[1]
		base := safeName(namespace + "-" + name)
		kube("describe-"+base, "describe", "pod", name, "-n", namespace)
		kube("logs-"+base, "logs", name, "-n", namespace, "--all-containers", "--prefix", "--tail=500")
		kube("logs-previous-"+base, "logs", name, "-n", namespace, "--all-containers", "--prefix", "--previous", "--tail=500")
	}

	var releases []struct {
		Name      string `json:"name"`
		Namespace string `json:"namespace"`
	}
	if err := json.Unmarshal([]byte(helmJSON), &releases); err != nil {
		collected = append(collected, fmt.Errorf("decode Helm releases for diagnostics: %w", err))
	} else {
		for _, release := range releases {
			base := safeName(release.Namespace + "-" + release.Name)
			helm("helm-get-"+base, "get", "all", release.Name, "-n", release.Namespace)
			helm("helm-history-"+base, "history", release.Name, "-n", release.Namespace, "--output", "yaml")
			helm("helm-values-"+base, "get", "values", release.Name, "-n", release.Namespace, "--all")
			helm("helm-hooks-"+base, "get", "hooks", release.Name, "-n", release.Namespace)
			helm("helm-status-"+base, "status", release.Name, "-n", release.Namespace)
		}
	}

	if err := artifacts.Collect(collector.Artifacts, filepath.Join(collector.OutputDir, "component"), collector.Redactor); err != nil {
		collected = append(collected, err)
	}
	return errors.Join(collected...)
}

func safeName(value string) string {
	value = strings.NewReplacer("/", "-", "\\", "-", ":", "-").Replace(value)
	if len(value) > 150 {
		value = value[:150]
	}
	return value
}
