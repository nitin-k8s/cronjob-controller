package main

import (
    "context"
    "flag"
    "os"
	"fmt"

    appsv1 "k8s.io/api/apps/v1"
    batchv1 "k8s.io/api/batch/v1"
    "k8s.io/apimachinery/pkg/runtime"
    utilruntime "k8s.io/apimachinery/pkg/util/runtime"
    clientgoscheme "k8s.io/client-go/kubernetes/scheme"
    ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
    "sigs.k8s.io/controller-runtime/pkg/healthz"
    "go.opentelemetry.io/otel"

    "github.com/example/cronjob-controller/controllers"
)

var (
    scheme   = runtime.NewScheme()
    setupLog = ctrl.Log.WithName("setup")
)

func init() {
    utilruntime.Must(clientgoscheme.AddToScheme(scheme))
    utilruntime.Must(appsv1.AddToScheme(scheme))
    utilruntime.Must(batchv1.AddToScheme(scheme))
}

func main() {
    var metricsAddr string
    var enableLeaderElection bool
    var probeAddr string
	fmt.Println("Starting cronjob-controller...")
    flag.StringVar(&metricsAddr, "metrics-addr", ":8080", "The address the metric endpoint binds to.")
    flag.StringVar(&probeAddr, "health-probe-addr", ":8081", "The address the probe endpoint binds to.")
    flag.BoolVar(&enableLeaderElection, "leader-elect", false,
        "Enable leader election for controller manager.")
    flag.Parse()
	ctrl.SetLogger(zap.New(zap.UseDevMode(true)))

    // Optionally restrict controller to a single namespace by setting WATCH_NAMESPACE
    // environment variable. If empty, the controller watches all namespaces.
    //watchNamespace := os.Getenv("WATCH_NAMESPACE")
	watchNamespace := "default"

	fmt.Println("Creating manager...")
    mgrOpts := ctrl.Options{
        Scheme:                 scheme,
        MetricsBindAddress:     metricsAddr,
        Port:                   9443,
        HealthProbeBindAddress: probeAddr,
        LeaderElection:         enableLeaderElection,
        LeaderElectionID:       "cronjob-controller.example.com",
    }
    if watchNamespace != "" {
        mgrOpts.Namespace = watchNamespace
    }

	fmt.Println("Initializing manager...")
    mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), mgrOpts)
    if err != nil {
        setupLog.Error(err, "unable to start manager")
        os.Exit(1)
    }

	fmt.Println("Registering Deployment controller with manager...")
    if err = (&controllers.DeploymentReconciler{
        Client:   mgr.GetClient(),
        Scheme:   mgr.GetScheme(),
        Recorder: mgr.GetEventRecorderFor("cronjob-controller"),
        Tracer:   otel.Tracer("cronjob-controller"),
    }).SetupWithManager(mgr); err != nil {
        setupLog.Error(err, "unable to create controller", "controller", "Deployment")
        os.Exit(1)
    }

    if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
        setupLog.Error(err, "unable to set up health check")
        os.Exit(1)
    }
    if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
        setupLog.Error(err, "unable to set up ready check")
        os.Exit(1)
    }

    setupLog.Info("starting manager")
    if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
        setupLog.Error(err, "problem running manager")
        os.Exit(1)
    }
    _ = context.Background()
}
