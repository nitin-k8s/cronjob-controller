# CronJob Image Sync Controller

This small controller watches `Deployment` resources and synchronizes images into related `CronJob` templates. When a `Deployment` image changes it:

- updates the image(s) in associated `CronJob.spec.jobTemplate` containers
- deletes existing `Job` objects owned by the `CronJob` so new runs pick up the updated image

Matching between a `Deployment` and `CronJob` is done by one of:

- label on the CronJob: `managed-by-deployment=<deployment-name>`
- annotation on the CronJob: `controller.example.com/managed-by-deployment=<namespace>/<name>`
- or by image equality fallback (if CronJob uses same image string as deployment)

Quick start

Build:

```bash
go build ./...
```

Run (inside cluster, recommended): build an image and run as Deployment. Ensure RBAC from `config/rbac/role.yaml` is applied.

Design notes

- Implemented with `sigs.k8s.io/controller-runtime` for extensibility.
- The reconciler is `DeploymentReconciler` and can be extended to handle other resource updates.
