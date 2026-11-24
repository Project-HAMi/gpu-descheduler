package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	_ "k8s.io/client-go/plugin/pkg/client/auth"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	v1 "gpu-descheduler/pkg/gpu-descheduler/api/v1"
	"gpu-descheduler/pkg/gpu-descheduler/cache"
	"gpu-descheduler/pkg/gpu-descheduler/controller"
	"gpu-descheduler/pkg/gpu-descheduler/defrag"
	defragwebhook "gpu-descheduler/pkg/gpu-descheduler/webhook"
	"gpu-descheduler/pkg/schedulerclient"
)

const defaultTLSDir = "/etc/webhook/certs"

var (
	setupLog = ctrl.Log.WithName("setup")

	Version   = "dev"
	BuildTime = "unknown"
	GitCommit = "unknown"
	GoVersion = "unknown"
)

const (
	defaultSchedulerNamespace = "kube-system"
	defaultSchedulerService   = "hami-scheduler"
	defaultMetricsPort        = 9395
)

// discoverSchedulerURL finds the HAMi scheduler metrics endpoint.
func discoverSchedulerURL(namespace, service string, port int) string {
	var client kubernetes.Interface
	config, err := rest.InClusterConfig()
	if err == nil {
		client, err = kubernetes.NewForConfig(config)
	}
	if err != nil {
		setupLog.Error(err, "Cannot build an in-cluster client for scheduler discovery")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	url, source := schedulerclient.DiscoverURL(ctx, client, namespace, service, port)
	setupLog.Info("Discovered HAMi scheduler metrics endpoint",
		"schedulerURL", url,
		"source", source,
		"service", namespace+"/"+service,
		"metricsPort", port)
	return url
}

func main() {
	var tlsDir string
	var port int
	var metricsAddr string
	var schedulerPort int
	var schedulerNamespace, schedulerService string
	var syncInterval time.Duration

	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "The address the metric endpoint binds to.")
	flag.StringVar(&metricsAddr, "metrics-addr", ":8080", "Deprecated: use --metrics-bind-address.")
	flag.IntVar(&schedulerPort, "scheduler-port", defaultMetricsPort, "HAMi scheduler metrics container port")
	flag.StringVar(&schedulerNamespace, "scheduler-namespace", defaultSchedulerNamespace, "Namespace of the HAMi scheduler Service")
	flag.StringVar(&schedulerService, "scheduler-service", defaultSchedulerService, "Name of the HAMi scheduler Service")
	flag.DurationVar(&syncInterval, "sync-interval", 1*time.Minute, "Interval for syncing cluster state from scheduler")
	var enableLeaderElection bool
	flag.StringVar(&tlsDir, "tls-dir", defaultTLSDir, "tls certificate directory")
	flag.IntVar(&port, "port", 8443, "webhook https listen port")
	flag.IntVar(&port, "webhook-port", 8443, "Deprecated: use --port.")
	flag.BoolVar(&enableLeaderElection, "enable-leader-election", true, "enable leader election for HA")
	var defragEnabled bool
	var defragNamespaces, defragMode, defragScope string
	defragOpts := defrag.DefaultOptions()
	flag.BoolVar(&defragEnabled, "defrag-enabled", false, "Plan GPU defragmentation for unschedulable HAMi pods")
	flag.StringVar(&defragMode, "defrag-mode", string(defragOpts.Mode), "dryRun only reports plans; execute also carries them out")
	flag.StringVar(&defragScope, "defrag-scope", string(defragOpts.Scope), "sameNode keeps moved pods on their node; crossNode lets them move to another node")
	flag.DurationVar(&defragOpts.Interval, "defrag-interval", defragOpts.Interval, "Interval between defrag runs")
	flag.IntVar(&defragOpts.MaxMoves, "defrag-max-moves", defragOpts.MaxMoves, "Largest number of moves in one plan")
	flag.IntVar(&defragOpts.MaxPlans, "defrag-max-plans", defragOpts.MaxPlans, "Largest number of unschedulable pods planned for per run")
	flag.IntVar(&defragOpts.Alternatives, "defrag-alternatives", defragOpts.Alternatives, "Plans reported besides the recommended one")
	flag.DurationVar(&defragOpts.MinPendingAge, "defrag-min-pending-age", defragOpts.MinPendingAge, "How long a pod must be unschedulable before it is planned for")
	flag.DurationVar(&defragOpts.Cooldown, "defrag-cooldown", defragOpts.Cooldown, "How long a workload is not moved again after a move")
	flag.DurationVar(&defragOpts.MoveTimeout, "defrag-move-timeout", defragOpts.MoveTimeout, "Time a move, and the wait for the pod after the last move, may take")
	flag.StringVar(&defragNamespaces, "defrag-namespaces", "", "Comma-separated namespaces whose pods may be moved; empty means all")
	flag.Int64Var(&defragOpts.DefaultMemoryMB, "defrag-default-memory", 0, "The HAMi scheduler's nvidia.defaultMemory, in MB")
	flag.Int64Var(&defragOpts.DefaultCores, "defrag-default-cores", 0, "The HAMi scheduler's nvidia.defaultCores")
	flag.Int64Var(&defragOpts.MemoryFactor, "defrag-memory-factor", defragOpts.MemoryFactor, "The HAMi scheduler's nvidia.memoryFactor")
	klog.InitFlags(nil)
	flag.Parse()
	defragOpts.Mode, defragOpts.Scope = defrag.Mode(defragMode), defrag.Scope(defragScope)
	defragOpts.Namespaces = defrag.ParseNamespaces(defragNamespaces)
	if err := defragOpts.Validate(); defragEnabled && err != nil {
		log.Fatalf("invalid defrag flags: %v", err)
	}

	ctrl.SetLogger(klog.NewKlogr())

	schedulerURL := discoverSchedulerURL(schedulerNamespace, schedulerService, schedulerPort)

	setupLog.Info("====================================",
		"component", "GPU Descheduler Webhook",
		"version", Version,
		"buildTime", BuildTime,
		"gitCommit", GitCommit,
		"goVersion", GoVersion,
	)
	setupLog.Info("====================================")

	// Create Scheme
	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	_ = v1.AddToScheme(scheme)

	// Create Manager
	setupLog.Info("Starting gpu-descheduler webhook server",
		"webhookPort", port,
		"schedulerURL", schedulerURL,
		"syncInterval", syncInterval)

	placementCache := cache.InitGlobalCache()
	setupLog.Info("Global placement cache initialized")

	if err := cache.LoadConsumedReqIDsFromFile(); err != nil {
		setupLog.Error(err, "Failed to load persisted ReqID state, idempotency may be compromised after restart")
	} else {
		setupLog.Info("Successfully loaded persisted ReqID state (idempotency guaranteed across restarts)")
	}

	ctx := ctrl.SetupSignalHandler()
	cache.StartConsumedReqIDCleanup(ctx.Done())
	setupLog.Info("Started consumed ReqID cleanup task (TTL: 24h, interval: 1h)")

	rediscoverScheduler := func() string {
		newURL := discoverSchedulerURL(schedulerNamespace, schedulerService, schedulerPort)
		setupLog.Info("Rediscovering scheduler URL due to connection failures",
			"newURL", newURL,
			"port", schedulerPort)
		return newURL
	}

	clusterState := controller.StartClusterStateSync(schedulerURL, syncInterval, rediscoverScheduler)
	setupLog.Info("Cluster state sync started with auto-rediscovery enabled",
		"schedulerURL", schedulerURL,
		"syncInterval", syncInterval,
		"feature", "Auto-rediscover scheduler IP on consecutive failures (threshold: 3)")

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme: scheme,
		Metrics: metricsserver.Options{
			BindAddress: metricsAddr,
		},
		HealthProbeBindAddress: ":8081",
		LeaderElection:         enableLeaderElection,
		LeaderElectionID:       "gpu-descheduler.hami.io",
		WebhookServer: webhook.NewServer(webhook.Options{
			Host:    "0.0.0.0",
			Port:    port,
			CertDir: tlsDir,
		}),
	})
	if err != nil {
		log.Fatalf("create manager failed: %v", err)
	}

	k8sClient := kubernetes.NewForConfigOrDie(ctrl.GetConfigOrDie())
	obsCache := cache.InitGlobalObservationCache(k8sClient)

	statusUpdater := func(namespace, name, phase, message string, matchedPod ...cache.MatchedPodInfo) error {
		ctx := context.Background()
		var pl v1.HamiPodPlacement
		if err := mgr.GetClient().Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, &pl); err != nil {
			return fmt.Errorf("failed to get placement %s/%s: %w", namespace, name, err)
		}

		now := metav1.Now()
		pl.Status.Phase = phase
		pl.Status.Message = message
		pl.Status.ConsumedAt = &now

		if len(matchedPod) > 0 && matchedPod[0].PodName != "" {
			pl.Status.MatchedPodName = matchedPod[0].PodName

			klog.InfoS("[MATCHED_POD_STATUS] Updated Placement with matched Pod name",
				"placement", namespace+"/"+name,
				"matchedPodName", matchedPod[0].PodName)
		}

		if err := mgr.GetClient().Status().Update(ctx, &pl); err != nil {
			return fmt.Errorf("failed to update placement status for %s/%s: %w", namespace, name, err)
		}

		klog.InfoS("Updated HamiPodPlacement CRD status via observation cache",
			"namespace", namespace,
			"name", name,
			"phase", phase,
			"message",
			message,
			"reqID",
			pl.Spec.ReqID)

		consumedInfo, exists := cache.GetReqIDConsumptionInfo(pl.Spec.ReqID)
		if exists {
			cache.MarkReqIDConsumed(
				pl.Spec.ReqID,
				namespace,
				name,
				consumedInfo.PodNamespace,
				consumedInfo.PodName,
				phase,
			)
			klog.V(3).InfoS("Synced ReqID consumption status with CRD phase",
				"reqID", pl.Spec.ReqID,
				"phase", phase)
		}

		return nil
	}

	obsCache.StartObserver(10*time.Second, 60, statusUpdater)
	setupLog.Info("Observation cache monitor started - will auto-detect pod deployment and update CRD status")

	reconciler := &controller.PlacementReconciler{
		Client:       mgr.GetClient(),
		Cache:        placementCache,
		ClusterState: clusterState,
	}
	if err := reconciler.SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to setup controller")
		os.Exit(1)
	}
	setupLog.Info("Controller registered successfully")

	var defragRunner *defrag.Runner
	if defragEnabled {
		defragRunner = defrag.NewRunner(mgr.GetClient(), mgr.GetAPIReader(), defragOpts)
		if err := mgr.Add(defragRunner); err != nil {
			setupLog.Error(err, "unable to add the defrag runner")
			os.Exit(1)
		}
		setupLog.Info("GPU defragmentation enabled", "mode", defragOpts.Mode, "scope", defragOpts.Scope)
	}

	decoder := admission.NewDecoder(scheme)

	podMutator := &defragwebhook.PodMutator{
		Client:       mgr.GetClient(),
		Decoder:      decoder,
		ClusterState: clusterState,
	}

	// Register Webhook
	mgr.GetWebhookServer().Register("/mutate", &webhook.Admission{Handler: podMutator})

	// Health checks
	if err := mgr.AddHealthzCheck("health", func(req *http.Request) error { return nil }); err != nil {
		log.Fatal(err)
	}
	if err := mgr.AddReadyzCheck("ready", func(req *http.Request) error { return nil }); err != nil {
		log.Fatal(err)
	}

	klog.Info("starting gpu-descheduler manager: controller + mutate webhook")
	setupLog.Info("Webhook registered successfully",
		"path", "/mutate-pod")

	apiMux := http.NewServeMux()
	RegisterAllAPIHandlers(apiMux, clusterState, placementCache)
	if defragRunner != nil {
		RegisterDefragAPIHandlers(apiMux, defragRunner)
	}

	apiServer := &http.Server{
		Addr:    ":8083",
		Handler: apiMux,
	}

	go func() {
		setupLog.Info("Starting API server (cluster-info + placements)", "addr", ":8083")
		if err := apiServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("API server error: %v", err)
		}
	}()

	setupLog.Info("starting gpu-descheduler webhook server")
	if err := mgr.Start(ctx); err != nil {
		log.Fatalf("manager exit error: %v", err)
	}
}
