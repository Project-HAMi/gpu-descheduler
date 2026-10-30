# HAMi GPU Descheduler Webhook

## Project Overview

**HAMi GPU Descheduler** is a specialized GPU defragmentation tool built for **HAMi**. It uses a **Mutating Admission Webhook** and **Custom Resource Definitions (CRDs)** to achieve fine-grained scheduling control over GPU workloads on HAMi virtualized nodes. It has the following features:

- **GPU Defragmentation**: Consolidates scattered GPU resources across nodes to improve cluster utilization
- **Precise Binding**: Schedules Pods to specific nodes and GPU devices
- **Active Eviction**: Supports evicting existing Pods and rescheduling them to target nodes


## Prerequisites

### Required Dependencies

| Component | Version/Status |
|-----------|---------------|
| **HAMi Scheduler** | v2.10.0+ |
| Kubernetes | v1.24+ (v1.28+ recommended) |
| Helm | v3.8+ |


## Build 

The project provides an automated build script. **Using the script is recommended** over manual `docker build`:

```bash
./build-image.sh
```

See [build-image.sh](build-image.sh) for details.

## Deployment

### Deployment with helm install

```bash
helm install gpu-descheduler . --namespace kube-system --create-namespace --debug
```

**Parameter Reference:**
| Parameter | Description |
|-----------|-------------|
| `--namespace` | Target deployment namespace |
| `--create-namespace` | Auto-create namespace if it doesn't exist |
| `--debug` | Show detailed debug information |
| `--timeout 5m` | Set timeout to 5 minutes |
| `-f values.yaml` | Use custom values file (optional) |


## Verifying the Deployment

```bash
kubectl get pods -n kube-system -l app.kubernetes/name=gpu-descheduler-webhook -w

# Expected output:
NAME                                      READY   STATUS    RESTARTS   AGE
gpu-descheduler-webhook-5f7d9c6b4d-abc12  1/1     Running   0          60s
gpu-descheduler-webhook-5f7d9c6b4d-def34  1/1     Running   0          60s
```

## Viewing Status with Curl

The webhook service provides **two core REST API endpoints**, listening on port **8083** inside the container:

| Endpoint | Purpose | Returned Information |
|----------|---------|---------------------|
| `/api/v1/cluster-info` | Cluster state information | Node list, GPU devices, health status, last sync time |
| `/api/v1/placements` | Placement CRD cache | Allocation records, consumption status, matched Pod info |


```bash
# Inside the container
curl -s http://localhost:8083/api/v1/cluster-info | jq .
```

## Usage
You need to create a CRD to guide this de-scheduler. The CRD manifest is `chart/templates/crd/hamipodplacement.yaml`. `helm install` applies it with the chart. The schema is maintained in that file. This repo has no `controller-gen` target for it.

Install the CRD on its own:

```bash
kubectl apply -f chart/templates/crd/hamipodplacement.yaml
kubectl get crd hamipodplacements.scheduling.hami.placement.com
```

One `HamiPodPlacement` is consumed by one Pod. The Pod must use `schedulerName: hami-scheduler` and match `podSelector`:

```yaml
apiVersion: scheduling.hami.placement.com/v1
kind: HamiPodPlacement
metadata:
  name: pin-gpu0
  namespace: default
spec:
  reqID: "pin-gpu0-001"          # min length 5, consumed once
  podSelector:
    matchLabels:
      app: my-job
  nodeName: gpu-node-1
  gpuDevices:
    - gpu-0                      # or gpu-0/70
  evictPod: my-job-6cd5964877-zdb5z   # Pod name in this namespace
```

```bash
kubectl get hamipodplacements -A
```
