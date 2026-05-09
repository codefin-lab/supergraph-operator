package main

import (
	"flag"
	"os"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	codefiniov1alpha1 "github.com/codefin/supergraph-operator/api/v1alpha1"
	"github.com/codefin/supergraph-operator/internal/controller"
)

var scheme = runtime.NewScheme()

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(codefiniov1alpha1.AddToScheme(scheme))
}

func main() {
	var metricsAddr string
	var probeAddr string
	var namespace string
	var federationVersion string
	var routerDeployment string
	var supergraphConfigMap string
	var roverPath string
	var leaderElect bool
	var leaderElectNamespace string
	var compositionTimeout time.Duration
	var dryRun bool
	var historyCount int

	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "The address the metric endpoint binds to.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.StringVar(&namespace, "namespace", "", "Namespace to watch. If empty, watches all namespaces.")
	flag.StringVar(&federationVersion, "federation-version", "=2.7.0", "Apollo Federation version for composition.")
	flag.StringVar(&routerDeployment, "router-deployment", "graph-router", "Name of the router Deployment to patch on composition.")
	flag.StringVar(&supergraphConfigMap, "supergraph-configmap", "graph-supergraph", "Name of the ConfigMap to store the composed supergraph.")
	flag.StringVar(&roverPath, "rover-path", "rover", "Path to the rover CLI binary.")
	flag.BoolVar(&leaderElect, "leader-elect", false, "Enable leader election for HA deployments.")
	flag.StringVar(&leaderElectNamespace, "leader-elect-namespace", "", "Namespace for the leader election Lease. Defaults to --namespace if set, otherwise uses the pod's namespace.")
	flag.DurationVar(&compositionTimeout, "composition-timeout", 2*time.Minute, "Timeout for rover compose execution.")
	flag.BoolVar(&dryRun, "dry-run", false, "Compose but don't update ConfigMap or Deployment.")
	flag.IntVar(&historyCount, "history-count", 0, "Number of previous supergraph versions to keep in ConfigMap (0=disabled).")

	opts := zap.Options{Development: true}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))
	logger := ctrl.Log.WithName("setup")

	mgrOpts := ctrl.Options{
		Scheme:                 scheme,
		HealthProbeBindAddress: probeAddr,
		Metrics: metricsserver.Options{
			BindAddress: metricsAddr,
		},
		LeaderElection:   leaderElect,
		LeaderElectionID: "supergraph-operator-leader",
	}

	// Resolve leader election namespace: explicit flag > --namespace > pod namespace (empty = auto-detect).
	leNs := leaderElectNamespace
	if leNs == "" {
		leNs = namespace
	}
	if leNs != "" {
		mgrOpts.LeaderElectionNamespace = leNs
	}

	if namespace != "" {
		mgrOpts.Cache.DefaultNamespaces = map[string]cache.Config{
			namespace: {},
		}
	}

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), mgrOpts)
	if err != nil {
		logger.Error(err, "unable to create manager")
		os.Exit(1)
	}

	reconciler := &controller.SubgraphSchemaReconciler{
		Client:              mgr.GetClient(),
		Scheme:              mgr.GetScheme(),
		Recorder:            mgr.GetEventRecorderFor("supergraph-operator"),
		FederationVersion:   federationVersion,
		RouterDeployment:    routerDeployment,
		SupergraphConfigMap: supergraphConfigMap,
		RoverPath:           roverPath,
		CompositionTimeout:  compositionTimeout,
		DryRun:              dryRun,
		HistoryCount:        historyCount,
	}

	if err := reconciler.SetupWithManager(mgr); err != nil {
		logger.Error(err, "unable to create controller", "controller", "SubgraphSchema")
		os.Exit(1)
	}

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		logger.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		logger.Error(err, "unable to set up ready check")
		os.Exit(1)
	}

	logger.Info("starting manager", "leaderElect", leaderElect, "dryRun", dryRun, "historyCount", historyCount)
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		logger.Error(err, "problem running manager")
		os.Exit(1)
	}
}
