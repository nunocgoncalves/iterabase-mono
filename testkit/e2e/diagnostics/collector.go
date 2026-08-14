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
	podsJSON := kube("kubernetes-pods", "get", "pods", "-A", "-o", "json")
	helmJSON := helm("helm-list", "list", "-A", "-o", "json")

	var pods struct {
		Items []struct {
			Metadata struct {
				Name      string `json:"name"`
				Namespace string `json:"namespace"`
			} `json:"metadata"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(podsJSON), &pods); err != nil {
		collected = append(collected, fmt.Errorf("decode pods for diagnostics: %w", err))
	} else {
		for _, pod := range pods.Items {
			base := safeName(pod.Metadata.Namespace + "-" + pod.Metadata.Name)
			kube("describe-"+base, "describe", "pod", pod.Metadata.Name, "-n", pod.Metadata.Namespace)
			kube("logs-"+base, "logs", pod.Metadata.Name, "-n", pod.Metadata.Namespace, "--all-containers", "--prefix", "--tail=500")
			kube("logs-previous-"+base, "logs", pod.Metadata.Name, "-n", pod.Metadata.Namespace, "--all-containers", "--prefix", "--previous", "--tail=500")
		}
	}

	var releases []struct {
		Name      string `json:"name"`
		Namespace string `json:"namespace"`
	}
	if err := json.Unmarshal([]byte(helmJSON), &releases); err != nil {
		collected = append(collected, fmt.Errorf("decode Helm releases for diagnostics: %w", err))
	} else {
		for _, release := range releases {
			helm("helm-get-"+safeName(release.Namespace+"-"+release.Name), "get", "all", release.Name, "-n", release.Namespace)
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
