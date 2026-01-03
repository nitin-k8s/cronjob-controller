package controllers

import (
    "context"
    "fmt"

    appsv1 "k8s.io/api/apps/v1"
    batchv1 "k8s.io/api/batch/v1"
    corev1 "k8s.io/api/core/v1"
    metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
    "k8s.io/apimachinery/pkg/runtime"
    "k8s.io/apimachinery/pkg/types"
    "k8s.io/client-go/tools/record"
    "github.com/prometheus/client_golang/prometheus"
    ctrl "sigs.k8s.io/controller-runtime"
    "sigs.k8s.io/controller-runtime/pkg/client"
    "sigs.k8s.io/controller-runtime/pkg/controller"
    "sigs.k8s.io/controller-runtime/pkg/log"

    "go.opentelemetry.io/otel"
    "go.opentelemetry.io/otel/trace"
)

// DeploymentReconciler watches Deployments and updates related CronJobs' images,
// then deletes existing Jobs so that new Jobs use updated CronJob template.
type DeploymentReconciler struct {
    client.Client
    Scheme   *runtime.Scheme
    Recorder record.EventRecorder
    // OpenTelemetry tracer and metric instruments
    Tracer trace.Tracer
}

var (
    syncsTotal = prometheus.NewCounterVec(
        prometheus.CounterOpts{
            Name: "cronjob_image_sync_reconciles_total",
            Help: "Total number of reconciles for deployments",
        },
        []string{"namespace", "deployment"},
    )
    cronjobsUpdated = prometheus.NewCounterVec(
        prometheus.CounterOpts{
            Name: "cronjob_image_sync_cronjobs_updated_total",
            Help: "Total number of cronjobs updated",
        },
        []string{"namespace", "cronjob"},
    )
    jobsDeleted = prometheus.NewCounterVec(
        prometheus.CounterOpts{
            Name: "cronjob_image_sync_jobs_deleted_total",
            Help: "Total number of jobs deleted by the controller",
        },
        []string{"namespace", "cronjob"},
    )
    syncErrors = prometheus.NewCounterVec(
        prometheus.CounterOpts{
            Name: "cronjob_image_sync_errors_total",
            Help: "Total number of errors during reconcile",
        },
        []string{"namespace", "deployment"},
    )
)

// SetupWithManager registers the reconciler with the manager.
func (r *DeploymentReconciler) SetupWithManager(mgr ctrl.Manager) error {
    // ensure metrics are registered (ignore AlreadyRegisteredError)
    _ = prometheus.Register(syncsTotal)
    _ = prometheus.Register(cronjobsUpdated)
    _ = prometheus.Register(jobsDeleted)
    _ = prometheus.Register(syncErrors)

    // set the event recorder if not provided
    if r.Recorder == nil {
        r.Recorder = mgr.GetEventRecorderFor("cronjob-controller")
    }

    // set tracer
    if r.Tracer == nil {
        r.Tracer = otel.Tracer("cronjob-controller")
    }

    return ctrl.NewControllerManagedBy(mgr).
        For(&appsv1.Deployment{}).
        WithOptions(controller.Options{MaxConcurrentReconciles: 2}).
        Complete(r)
}

// Reconcile reacts to Deployment changes.
func (r *DeploymentReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
    logger := log.FromContext(ctx)
    var deploy appsv1.Deployment
    if err := r.Get(ctx, req.NamespacedName, &deploy); err != nil {
        return ctrl.Result{}, client.IgnoreNotFound(err)
    }

    logger.Info("reconciling deployment", "deployment", req.NamespacedName)
    // metrics: record a reconcile invocation (Prometheus)
    syncsTotal.WithLabelValues(deploy.Namespace, deploy.Name).Inc()

    // Find CronJobs that declare they are managed by this deployment.
    cronjobs, err := r.findCronJobsForDeployment(ctx, &deploy)
    if err != nil {
        return ctrl.Result{}, err
    }

    // Build a map of container image by container name from the Deployment
    imageByName := make(map[string]string)
    for _, c := range deploy.Spec.Template.Spec.Containers {
        imageByName[c.Name] = c.Image
    }

    for _, cj := range cronjobs {
        updated := false
        // Update images on CronJob's job template
        for i, c := range cj.Spec.JobTemplate.Spec.Template.Spec.Containers {
            if img, ok := imageByName[c.Name]; ok {
                if c.Image != img {
                    cj.Spec.JobTemplate.Spec.Template.Spec.Containers[i].Image = img
                    updated = true
                }
            } else {
                // if no matching name, optionally sync first container image
                // (useful when names differ); fallback: use first deployment image
                if len(deploy.Spec.Template.Spec.Containers) > 0 {
                    fallback := deploy.Spec.Template.Spec.Containers[0].Image
                    if c.Image != fallback {
                        cj.Spec.JobTemplate.Spec.Template.Spec.Containers[i].Image = fallback
                        updated = true
                    }
                }
            }
        }

        if updated {
            if err := r.Update(ctx, &cj); err != nil {
                logger.Error(err, "failed to update cronjob", "cronjob", types.NamespacedName{Namespace: cj.Namespace, Name: cj.Name})
                syncErrors.WithLabelValues(deploy.Namespace, deploy.Name).Inc()
                r.Recorder.Event(&deploy, corev1.EventTypeWarning, "UpdateFailed", fmt.Sprintf("failed to update CronJob %s: %v", cj.Name, err))
                return ctrl.Result{}, err
            }
            cronjobsUpdated.WithLabelValues(cj.Namespace, cj.Name).Inc()
            r.Recorder.Event(&cj, corev1.EventTypeNormal, "CronJobUpdated", fmt.Sprintf("Updated job template images from Deployment %s/%s", deploy.Namespace, deploy.Name))

            // Delete existing Jobs created by this CronJob so new Jobs use updated image
            if err := r.deleteJobsForCronJob(ctx, &cj); err != nil {
                logger.Error(err, "failed to delete jobs for cronjob", "cronjob", cj.Name)
                syncErrors.WithLabelValues(deploy.Namespace, deploy.Name).Inc()
                r.Recorder.Event(&cj, corev1.EventTypeWarning, "DeleteJobsFailed", fmt.Sprintf("failed to delete Jobs for CronJob %s: %v", cj.Name, err))
                return ctrl.Result{}, err
            }

            logger.Info("updated cronjob image and deleted related jobs", "cronjob", cj.Name)
            r.Recorder.Event(&cj, corev1.EventTypeNormal, "JobsRecreated", "Deleted existing Jobs so future runs use the updated image")
        } else {
            logger.Info("cronjob already up-to-date", "cronjob", cj.Name)
        }
    }

    return ctrl.Result{}, nil
}

// findCronJobsForDeployment lists CronJobs in the Deployment namespace and returns those
// that indicate they are managed by the Deployment. Matching is done by:
// - label `managed-by-deployment=<deployment-name>` OR
// - annotation `controller.example.com/managed-by-deployment` with value `<ns>/<name>`
func (r *DeploymentReconciler) findCronJobsForDeployment(ctx context.Context, d *appsv1.Deployment) ([]batchv1.CronJob, error) {
    var list batchv1.CronJobList
    if err := r.List(ctx, &list, &client.ListOptions{Namespace: d.Namespace}); err != nil {
        return nil, err
    }

    var out []batchv1.CronJob
    for _, cj := range list.Items {
        if val, ok := cj.Labels["managed-by-deployment"]; ok && val == d.Name {
            out = append(out, cj)
            continue
        }
        if ann, ok := cj.Annotations["controller.example.com/managed-by-deployment"]; ok && ann == fmt.Sprintf("%s/%s", d.Namespace, d.Name) {
            out = append(out, cj)
            continue
        }
        // Also allow matching by image equality: if any container image in CronJob equals any in Deployment
        if hasSharedImage(&cj, d) {
            out = append(out, cj)
            continue
        }
    }
    return out, nil
}

func hasSharedImage(cj *batchv1.CronJob, d *appsv1.Deployment) bool {
    depImages := make(map[string]struct{})
    for _, c := range d.Spec.Template.Spec.Containers {
        depImages[c.Image] = struct{}{}
    }
    for _, c := range cj.Spec.JobTemplate.Spec.Template.Spec.Containers {
        if _, ok := depImages[c.Image]; ok {
            return true
        }
    }
    return false
}

// deleteJobsForCronJob deletes Jobs that are owned by the given CronJob using a foreground deletion
// so pods are removed as well.

func (r *DeploymentReconciler) deleteJobsForCronJob(ctx context.Context, cj *batchv1.CronJob) error {

    var jobList batchv1.JobList
    if err := r.List(ctx, &jobList, client.InNamespace(cj.Namespace)); err != nil {
        return err
    }

    propagationPolicy := metav1.DeletePropagationForeground

    for _, job := range jobList.Items {

        // Only jobs owned by this CronJob
        if !isOwnedByCronJob(&job, cj) {
            continue
        }

        jobName := job.Name

        // Delete the Job (foreground)
        if err := r.Delete(
            ctx,
            &job,
            &client.DeleteOptions{
                PropagationPolicy: &propagationPolicy,
            },
        ); err != nil  {
            return err
        }

        // Explicitly delete Pods created by this Job
        // This handles orphan Pods after image updates
        if err := r.DeleteAllOf(
            ctx,
            &corev1.Pod{},
            client.InNamespace(cj.Namespace),
            client.MatchingLabels{
                "job-name": jobName,
            },
        ); err != nil {
            return err
        }

        jobsDeleted.WithLabelValues(cj.Namespace, cj.Name).Inc()

        // Emit event (best-effort)
        r.Recorder.Eventf(
            cj,
            corev1.EventTypeNormal,
            "JobDeleted",
            "Deleted Job %s and its Pods for CronJob %s to allow new runs with updated image",
            jobName,
            cj.Name,
        )
    }

    return nil
}



func isOwnedByCronJob(job *batchv1.Job, cj *batchv1.CronJob) bool {
    for _, owner := range job.OwnerReferences {
        if owner.Kind == "CronJob" &&
            owner.Name == cj.Name &&
            owner.UID == cj.UID {
            return true
        }
    }
    return false
}
