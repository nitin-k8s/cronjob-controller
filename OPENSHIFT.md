# OpenShift build & deploy

This file contains step-by-step commands to build and deploy the controller on an OpenShift cluster.

1) Create project and apply RBAC + manifests

```bash
oc new-project cronjob-controller-system || true
kubectl apply -f config/rbac/role.yaml
kubectl apply -f config/openshift/controller-deployment.yaml
```

2) Build using OpenShift `Binary` build (recommended):

```bash
oc new-build --binary --name=cronjob-controller -l app=cronjob-controller
oc start-build cronjob-controller --from-dir=. --follow
oc new-app cronjob-controller
```

3) Or, build locally and push to a registry

```bash
docker build -t <registry>/myorg/cronjob-controller:latest .
docker push <registry>/myorg/cronjob-controller:latest
# replace image in config/openshift/controller-deployment.yaml and apply
kubectl apply -f config/openshift/controller-deployment.yaml
```

4) Confirm controller is running

```bash
kubectl -n cronjob-controller-system get pods
kubectl -n cronjob-controller-system logs deployment/cronjob-controller
```

5) Apply test nginx resources

```bash
kubectl apply -f config/samples/nginx-deployment.yaml
kubectl apply -f config/samples/nginx-cronjob.yaml
```

6) Trigger image update and validate sync

```bash
kubectl set image deployment/nginx-deployment nginx=nginx:1.22 -n default
kubectl get cronjob nginx-cronjob -n default -o yaml | yq '.spec.jobTemplate.spec.template.spec.containers'
kubectl get jobs -n default --selector=job-name
```

Notes:
- If using private registries, configure `imagePullSecrets` on the `cronjob-controller` Deployment.
- If OpenShift denies the chosen `runAsUser`, adjust SecurityContextConstraints or choose a suitable user/group per cluster policy.
