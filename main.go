package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
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
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	v1 "gpu-descheduler/pkg/gpu-descheduler/api/v1"
	"gpu-descheduler/pkg/gpu-descheduler/cache"
	"gpu-descheduler/pkg/gpu-descheduler/controller"
	defragwebhook "gpu-descheduler/pkg/gpu-descheduler/webhook"
	"gpu-descheduler/pkg/schedulerclient"
	"gpu-descheduler/pkg/webhookcert"
)

const (
	defaultTLSDir               = "/etc/webhook/certs"
	serviceAccountNamespaceFile = "/var/run/secrets/kubernetes.io/serviceaccount/namespace"
)

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

// podNamespace returns the namespace the Pod runs in.
func podNamespace() (string, error) {
	if ns := os.Getenv("POD_NAMESPACE"); ns != "" {
		return ns, nil
	}
	b, err := os.ReadFile(serviceAccountNamespaceFile)
	if err != nil {
		return "", err
	}
	if ns := strings.TrimSpace(string(b)); ns != "" {
		return ns, nil
	}
	return "", errors.New("empty namespace in " + serviceAccountNamespaceFile)
}

// webhookReadyCheck fails until ready is closed, then reports whether the
// webhook server accepts TLS connections. It asks for the server only then,
// because asking starts it, and it needs the certificate to start.
func webhookReadyCheck(ready <-chan struct{}, started func() healthz.Checker) healthz.Checker {
	check := sync.OnceValue(started)
	return func(req *http.Request) error {
		select {
		case <-ready:
			return check()(req)
		default:
			return errors.New("webhook certificate is not ready yet")
		}
	}
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
	var manageCerts bool
	var certSecret, webhookService, webhookConfig string
	flag.StringVar(&tlsDir, "tls-dir", defaultTLSDir, "tls certificate directory")
	flag.IntVar(&port, "port", 8443, "webhook https listen port")
	flag.IntVar(&port, "webhook-port", 8443, "Deprecated: use --port.")
	flag.BoolVar(&enableLeaderElection, "enable-leader-election", true, "enable leader election for HA")
	flag.BoolVar(&manageCerts, "manage-certs", true, "Issue and renew the webhook certificate into --tls-dir. Disable when cert-manager mounts it there.")
	flag.StringVar(&certSecret, "cert-secret", "gpu-descheduler-webhook-cert", "Secret in the Pod namespace that stores the managed certificate")
	flag.StringVar(&webhookService, "webhook-service", "gpu-descheduler-webhook", "Service the managed certificate is issued for")
	flag.StringVar(&webhookConfig, "webhook-config", "gpu-descheduler-webhook", "MutatingWebhookConfiguration that gets the CA of the managed certificate")
	klog.InitFlags(nil)
	flag.Parse()

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

	var certsReady <-chan struct{}
	if manageCerts {
		namespace, err := podNamespace()
		if err != nil {
			log.Fatalf("find the Pod namespace for the webhook certificate: %v", err)
		}
		certs := webhookcert.New(k8sClient, namespace, certSecret, webhookService, webhookConfig, tlsDir)
		if err := mgr.Add(certs); err != nil {
			log.Fatalf("add webhook certificate manager: %v", err)
		}
		certsReady = certs.Ready()
	} else {
		ready := make(chan struct{})
		close(ready)
		certsReady = ready
	}

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

	decoder := admission.NewDecoder(scheme)

	podMutator := &defragwebhook.PodMutator{
		Client:       mgr.GetClient(),
		Decoder:      decoder,
		ClusterState: clusterState,
	}

	// The webhook server loads its certificate when it starts, so it is
	// added to the manager only once the certificate is written.
	go func() {
		<-certsReady
		mgr.GetWebhookServer().Register("/mutate", &webhook.Admission{Handler: podMutator})
	}()

	// Health checks
	if err := mgr.AddHealthzCheck("health", func(req *http.Request) error { return nil }); err != nil {
		log.Fatal(err)
	}
	webhookStarted := func() healthz.Checker { return mgr.GetWebhookServer().StartedChecker() }
	if err := mgr.AddReadyzCheck("webhook", webhookReadyCheck(certsReady, webhookStarted)); err != nil {
		log.Fatal(err)
	}

	klog.Info("starting gpu-descheduler manager: controller + mutate webhook")

	apiMux := http.NewServeMux()
	RegisterAllAPIHandlers(apiMux, clusterState, placementCache)

	apiServer := newAPIServer(":8083", apiMux)

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
