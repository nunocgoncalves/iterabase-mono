//go:build e2e

// Command e2econtroller runs only the production AgentPool reconciler against a
// real test cluster. It deliberately has no gateway Store so storage E2E can
// exercise the portable controller/PVC/worker path without a product database.
package main

import (
	"os"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/nunocgoncalves/iterabase-mono/control-plane/api/v1alpha1"
	"github.com/nunocgoncalves/iterabase-mono/control-plane/internal/controller"
)

func main() {
	ctrl.SetLogger(zap.New(zap.UseDevMode(true)))
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(v1alpha1.AddToScheme(scheme))

	manager, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsserver.Options{BindAddress: "0"},
		HealthProbeBindAddress: "0",
		LeaderElection:         false,
	})
	if err != nil {
		ctrl.Log.Error(err, "create AgentPool E2E manager")
		os.Exit(1)
	}
	if err := (&controller.AgentPoolReconciler{
		Client:    manager.GetClient(),
		Scheme:    manager.GetScheme(),
		APIReader: manager.GetAPIReader(),
	}).SetupWithManager(manager); err != nil {
		ctrl.Log.Error(err, "register AgentPool reconciler")
		os.Exit(1)
	}
	if err := manager.Start(ctrl.SetupSignalHandler()); err != nil {
		ctrl.Log.Error(err, "run AgentPool E2E manager")
		os.Exit(1)
	}
}
