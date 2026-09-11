# HAMi GPU Descheduler Webhook - Complete Deployment Guide

## Table of Contents

1. [Project Overview](#project-overview)
2. [Prerequisites](#prerequisites)
3. [Building the Helm Package](#building-the-helm-package)
4. [One-Click Deployment](#one-click-deployment)
5. [Verifying the Deployment](#verifying-the-deployment)
6. [Viewing Status with Curl](#viewing-status-with-curl)
7. [FAQ](#faq)

---

## Project Overview

**HAMi GPU Descheduler** is a specialized GPU defragmentation tool built for the **HAMi gpu virtualization environment**. It uses a **Mutating Admission Webhook** and **Custom Resource Definitions (CRDs)** to achieve fine-grained scheduling control over GPU workloads on HAMi virtualized nodes.

> **⚠️ This project MUST run on top of the HAMi virtualization environment. It CANNOT be deployed independently without HAMi Scheduler.**
>
> - ✅ Applicable scope: Only for **HAMi virtualized nodes** and their **vGPU Pods**
> - ❌ Not applicable: Native Kubernetes GPU nodes, non-HAMi managed workloads
> - 🔗 Dependency: Must be used together with [HAMi Scheduler](https://github.com/Project-HAMi/HAMi)
>
> **How it works:**
> 1. Fetches cluster GPU virtualization state from HAMi Scheduler
> 2. Monitors scheduling and allocation of HAMi vGPU Pods
> 3. Performs cross-node GPU defragmentation via Placement CRDs
> 4. Only processes GPU workloads scheduled through HAMi Scheduler

### Core Features

- ✅ **GPU Defragmentation**: Consolidates scattered GPU resources across nodes to improve cluster utilization
- ✅ **Precise Binding**: Schedules Pods to specific nodes and GPU devices
- ✅ **Active Eviction**: Supports evicting existing Pods and rescheduling them to target nodes
- ✅ **Idempotency Guarantee**: Ensures traceability and prevents duplicate execution via ReqID
- ✅ **Auto Discovery**: Automatically discovers HAMi Scheduler Pod IP, no manual configuration needed
- ✅ **State Monitoring**: Provides REST API for querying cluster state and allocation records

---

## Prerequisites

### Required Dependencies (Must Be Met)

| Component | Version/Status | Description |
|-----------|---------------|-------------|
| **HAMi Scheduler** | ✅ **Deployed and running** | Core dependency, **must be deployed before this project** |
| **HAMi Virtualized Nodes** | ✅ At least 1 node with HAMi vGPU enabled | Target management objects |
| Kubernetes | v1.24+ (v1.28+ recommended) | Base runtime environment |
| Helm | v3.8+ | Deployment tool |
| Docker | Required for building images | Build-time dependency |

> **⚠️ Critical Prerequisites**: Before deploying this project, ensure:
>
> 1. **HAMi Scheduler is successfully deployed**: `kubectl get pods -A | grep hami-scheduler` should show Running
> 2. **Nodes have HAMi virtualization enabled**: HAMi Device Plugin should exist on nodes (`kubectl get pods -A | grep hami-device-plugin`)
> 3. **HAMi Scheduler Metrics are accessible**: Default port `9395`, used to fetch cluster GPU state
>
> **If the above conditions are not met, please set up the HAMi environment first:**
> - 📘 [HAMi Official Documentation](https://github.com/Project-HAMi/HAMi/wiki)
> - 🚀 [HAMi Quick Deployment Guide](https://github.com/Project-HAMi/HAMi/blob/main/docs/zh-CN/installation.md)

### Auto-Managed Components

| Component | Description | Manual Installation Required |
|-----------|-------------|------------------------------|
| TLS Certificate | Auto-generated using kube-webhook-certgen | ❌ No (automatic) |
| CRD Definition | HamiPodPlacement resource definition | ❌ No (automatic) |
| RBAC Permissions | ClusterRole/ServiceAccount for Webhook | ❌ No (automatic) |
| MutatingWebhook | Admission controller registration | ❌ No (automatic) |

> **✅ Note**: Apart from the "Required Dependencies" above, all other components are **automatically created and managed** by the Helm Chart. No manual intervention needed.

---

## Building the Helm Package

### Step 1: Build the Image (using build-image.sh)

The project provides an automated build script. **Using the script is recommended** over manual `docker build`:

```bash
cd /path/to/gpu-descheduler

# Basic usage - build with default configuration
./build-image.sh

# Specify a version tag
./build-image.sh -t v0.0.2

# Build and push to registry
./build-image.sh -t v0.0.2 --push

# Multi-arch build (amd64 + arm64)
./build-image.sh -t latest -p all --push

# Show help
./build-image.sh -h
```

**Script Features:**
- ✅ Auto-injects Git version info and build timestamp
- ✅ Supports multi-arch builds (linux/amd64, linux/arm64)
- ✅ Auto-pushes to Harbor image registry
- ✅ Supports cache control (`--no-cache`)
- ✅ Detailed build log output

**Common Parameters:**
```bash
Options:
  -t, --tag <tag>          Image tag (default: v0.0.2)
  -r, --registry <url>     Image registry URL prefix
  -p, --platform <arch>    Target platform (linux/amd64, linux/arm64, all)
  -v, --version <ver>      Version number injected into binary
  --push                   Push image after build
  --no-cache               Build without Docker cache
  -h, --help               Show help info
```

See [build-image.sh](build-image.sh) for details.

### Step 2: Package the Helm Chart

```bash
cd chart

# View current chart structure
tree .
# Output:
# .
# ├── Chart.yaml
# ├── values.yaml
# └── templates/
#     ├── deployment.yaml
#     ├── mutating-webhook.yaml
#     ├── service.yaml
#     ├── rbac/
#     │   ├── clusterrole.yaml
#     │   ├── clusterrolebinding.yaml
#     │   └── sa.yaml
#     ├── crd/
#     │   └── hamipodplacement.yaml
#     └── tls-cert.yaml

# Package the Helm Chart (generates .tgz file)
helm package .

# Output: gpu-descheduler-webhook-0.1.0.tgz
```

### Step 3: (Optional) Push to Helm Repository

```bash
# If you have a private Helm repository
helm push gpu-descheduler-webhook-0.1.0.tgz http://your-helm-repo/

# Or deploy directly using the local file
```

---

## One-Click Deployment

### Pre-Deployment Checks

```bash
# 1. Check K8s cluster connectivity
kubectl cluster-info

# 2. Check HAMi Scheduler is running
kubectl get pods -A | grep hami-scheduler
kubectl get svc -A | grep hami-scheduler
```

### Configure Deployment Parameters

> **⚠️ Important**: All deployment parameters are maintained in [chart/values.yaml](chart/values.yaml). **No separate deploy-values.yaml file is needed.**

**To customize configuration, simply edit `chart/values.yaml`:**

```bash
cd chart

# View default configuration
cat values.yaml | head -50

# Modify the following key parameters for your environment:
#   - image.repository / image.tag        # Image address and version
#   - namespace                             # Deployment namespace
#   - replicaCount                          # Replicas (⚠️ Must be 1, multi-instance not supported)
#   - resources.requests/limits             # Resource limits
#   - args                                  # Container startup arguments
#   - patch.enabled                         # certgen auto certificate generation toggle
```

**Common Customization Scenarios:**

| Scenario | Parameters to Modify |
|----------|---------------------|
| Use private image registry | `image.repository`, `image.tag`, `image.pullSecrets` |
| Adjust resource quotas | `resources.requests.cpu/memory`, `resources.limits.cpu/memory` |
| Change log level | Adjust `-v=3` in `args` (0-5) |
| Disable auto certificate generation | `patch.enabled: false` |
| Customize sync interval | Adjust `--sync-interval=60s` in `args` |

> **Tip**: After modifying, no extra steps are needed — just run the install command below.

### Execute One-Click Deployment

```bash
cd chart

# Standard install command (recommended)
helm install gpu-descheduler . --namespace kube-system --create-namespace --debug

# Using a custom values file (optional)
helm install gpu-descheduler . \
  -f values.yaml \
  --namespace kube-system \
  --create-namespace \
  --debug

# Deploy using a packaged .tgz file
# (See "Step 2: Package the Helm Chart" above)
helm install gpu-descheduler ./gpu-descheduler-webhook-0.1.0.tgz \
  --namespace kube-system \
  --create-namespace \
  --debug \
  --timeout 5m
```

**Parameter Reference:**
| Parameter | Description |
|-----------|-------------|
| `--namespace` | Target deployment namespace |
| `--create-namespace` | Auto-create namespace if it doesn't exist |
| `--debug` | Show detailed debug information |
| `--timeout 5m` | Set timeout to 5 minutes |
| `-f values.yaml` | Use custom values file (optional) |

> **Tip**: Always use `--timeout 5m` to avoid long waits due to network issues.

### Successful Deployment Output

```bash
# The following output indicates successful deployment:
NAME: gpu-descheduler
LAST DEPLOYED: 2026-08-28 10:30:00
NAMESPACE: kube-system
STATUS: deployed
REVISION: 1
TEST SUITE: None
NOTES:
...
```

---

## Verifying the Deployment

### 1. Check Pod Status

```bash
kubectl get pods -n kube-system -l app.kubernetes/name=gpu-descheduler-webhook -w

# Expected output:
NAME                                      READY   STATUS    RESTARTS   AGE
gpu-descheduler-webhook-5f7d9c6b4d-abc12  1/1     Running   0          60s
gpu-descheduler-webhook-5f7d9c6b4d-def34  1/1     Running   0          60s
```

### 2. Check Webhook Registration

```bash
kubectl get mutatingwebhookconfiguration | grep defrag

# Expected output:
gpu-descheduler-mutating-webhook   2026-08-28T10:30:00Z   1
```

### 3. Check TLS Certificate Auto-Generation

```bash
# View the TLS Secret created by certgen (auto-generated by Helm)
kubectl get secret gpu-descheduler-webhook-tls -n kube-system

# Expected output:
NAME                          TYPE                DATA   AGE
gpu-descheduler-webhook-tls   kubernetes.io/tls   2      10m

# View certgen Job status (should show Completed)
kubectl get jobs -n kube-system | grep create-secret

# Expected output:
gpu-descheduler-webhook-create-secret   1/1           10m
```

> **Note**: TLS certificates are **auto-generated** by **kube-webhook-certgen** during `helm install`. No manual operation needed.

### 4. Check Service Endpoints

```bash
kubectl get endpoints gpu-descheduler-webhook -n kube-system

# Expected output:
NAME                         ENDPOINTS                           AGE
gpu-descheduler-webhook      10.244.x.x:8443,10.244.y.y:8443   1m
```

### 5. View Pod Logs

```bash
# View webhook startup logs
kubectl logs -f -n kube-system -l app.kubernetes/name=gpu-descheduler-webhook

# You should see output like:
# 1. "Registered HTTP endpoint: GET /api/v1/cluster-info"
# 2. "Registered HTTP endpoint: GET /api/v1/placements"
# 3. "Starting API server (cluster-info + placements)" addr=":8083"
# 4. "starting gpu-descheduler manager: controller + mutate webhook"
```

---

## Viewing Status with Curl

The webhook service provides **two core REST API endpoints**, listening on port **8083** inside the container:

| Endpoint | Purpose | Returned Information |
|----------|---------|---------------------|
| `/api/v1/cluster-info` | Cluster state information | Node list, GPU devices, health status, last sync time |
| `/api/v1/placements` | Placement CRD cache | Allocation records, consumption status, matched Pod info |

### Method 1: Exec into Container (Recommended)

This is the most direct approach — no port forwarding needed.

#### Step 1: Get Pod Name

```bash
kubectl get pods -n kube-system -l app.kubernetes/name=gpu-descheduler-webhook

# Example output:
NAME                                      READY   STATUS    RESTARTS   AGE
gpu-descheduler-webhook-5f7d9c6b4d-abc12  1/1     Running   0          10m
```

#### Step 2: Exec into Container

```bash
# Use the Pod name from the previous step
kubectl exec -it -n kube-system gpu-descheduler-webhook-5f7d9c6b4d-abc12 -- /bin/bash

# If the container doesn't have bash, try sh
kubectl exec -it -n kube-system gpu-descheduler-webhook-5f7d9c6b4d-abc12 -- /bin/sh
```

#### Step 3: View Cluster Info

```bash
# Inside the container
curl -s http://localhost:8083/api/v1/cluster-info | jq .

# Example response:
{
  "timestamp": "2026-08-28T10:30:00Z",
  "nodeCount": 3,
  "totalGPUs": 8,
  "healthy": true,
  "lastSyncAt": "2026-08-28T10:29:50Z",
  "syncError": "",
  "nodes": [
    {
      "nodeName": "k8s-node01",
      "healthy": true,
      "gpuCount": 4,
      "labels": {
        "node-type": "gpu"
      },
      "devices": [
        {
          "id": "0",
          "index": 0,
          "type": "A100-80G",
          "vendor": "NVIDIA",
          "memoryMB": 81920,
          "cores": 6912,
          "numa": 0,
          "mode": "",
          "health": true
        },
        {
          "id": "1",
          "index": 1,
          "type": "A100-80G",
          "vendor": "NVIDIA",
          "memoryMB": 81920,
          "cores": 6912,
          "numa": 0,
          "mode": "",
          "health": true
        }
      ]
    },
    {
      "nodeName": "k8s-node02",
      "healthy": true,
      "gpuCount": 2,
      "labels": {},
      "devices": [...]
    },
    {
      "nodeName": "k8s-node03",
      "healthy": false,
      "gpuCount": 2,
      "labels": {},
      "devices": [...]
    }
  ]
}
```

**Field Reference:**

| Field | Type | Description |
|-------|------|-------------|
| `nodeCount` | int | Total number of nodes in the cluster |
| `totalGPUs` | int | Total number of GPU devices |
| `healthy` | bool | Overall health status (true when all nodes are healthy) |
| `lastSyncAt` | string | Timestamp of last data sync from Scheduler |
| `syncError` | string | Sync error message (empty when no error) |
| `nodes[].nodeName` | string | Node name |
| `nodes[].healthy` | bool | Node health status |
| `nodes[].gpuCount` | int | Number of GPUs on this node |
| `nodes[].devices[]` | array | GPU device details list |
| `devices[].id` | string | Device ID |
| `devices[].uuid` | string | GPU UUID (unique identifier) |
| `devices[].type` | string | GPU model (e.g., A100-80G) |
| `devices[].memoryMB` | int32 | Memory size in MB |
| `devices[].health` | bool | Device health status |

#### Step 4: View Placement Cache

```bash
# Inside the container
curl -s http://localhost:8083/api/v1/placements | jq .

# Example response:
{
  "timestamp": "2026-08-28T10:30:00Z",
  "totalCount": 3,
  "placements": [
    {
      "namespace": "default",
      "name": "immediate-migrate-pod-001",
      "reqID": "migration-20260828-001",
      "nodeName": "k8s-node01",
      "gpuDevices": ["0", "1"],
      "phase": "Consumed",
      "message": "Successfully matched to pod/train-job-xyz",
      "createdAt": "2026-08-28T10:25:00Z",
      "consumedAt": "2026-08-28T10:26:30Z",
      "matchedPodName": "train-job-xyz",
      "assignments": [
        {
          "podName": "train-job-xyz",
          "gpuDevice": "GPU-uuid-12345"
        }
      ]
    },
    {
      "namespace": "ml-team",
      "name": "deploy-model-v2",
      "reqID": "deploy-20260828-002",
      "nodeName": "k8s-node02",
      "gpuDevices": ["0"],
      "phase": "Pending",
      "message": "Waiting for pod creation",
      "createdAt": "2026-08-28T10:28:00Z",
      "consumedAt": null,
      "matchedPodName": null,
      "assignments": []
    }
  ]
}
```

**Field Reference:**

| Field | Type | Description |
|-------|------|-------------|
| `totalCount` | int | Total number of Placements in cache |
| `placements[].namespace` | string | Placement namespace |
| `placements[].name` | string | Placement CRD name |
| `placements[].reqID` | string | Request ID (for idempotency guarantee) |
| `placements[].nodeName` | string | Target node name |
| `placements[].gpuDevices[]` | array | List of assigned GPU device indices |
| `placements[].phase` | string | Phase state (Pending/Consumed/Failed) |
| `placements[].message` | string | Status description message |
| `placements[].createdAt` | string | Creation timestamp |
| `placements[].consumedAt` | string | Consumption timestamp (null if not consumed) |
| `placements[].matchedPodName` | string | Matched Pod name (null if not matched) |
| `placements[].assignments[]` | array | Specific Pod-GPU assignment records |

#### Step 5: Exit Container

```bash
exit
```

### Method 2: Using kubectl Port-Forward

If you prefer not to enter the container, you can use port-forwarding to access from your local machine.

```bash
# Start port-forwarding (map container port 8083 to local)
kubectl port-forward svc/gpu-descheduler-webhook 8083:8083 -n kube-system &

# In another terminal
curl -s http://localhost:8083/api/v1/cluster-info | jq .
curl -s http://localhost:8083/api/v1/placements | jq .

# Stop port-forwarding
kill %1  # or Ctrl+C
```

### Method 3: One-Shot Command (No Interactive Session)

```bash
# Query cluster info in one command
kubectl exec -n kube-system $(kubectl get pod -n kube-system -l app.kubernetes/name=gpu-descheduler-webhook -o jsonpath='{.items[0].metadata.name}') -- curl -s http://localhost:8083/api/v1/cluster-info | jq .

# Query Placement cache in one command
kubectl exec -n kube-system $(kubectl get pod -n kube-system -l app.kubernetes/name=gpu-descheduler-webhook -o jsonpath='{.items[0].metadata.name}') -- curl -s http://localhost:8083/api/v1/placements | jq .
```

### Quick Debug Commands Summary

```bash
# Set variable (simplifies subsequent commands)
POD_NAME=$(kubectl get pod -n kube-system -l app.kubernetes/name=gpu-descheduler-webhook -o jsonpath='{.items[0].metadata.name}')

# View cluster summary (key info only)
kubectl exec -n kube-system $POD_NAME -- curl -s http://localhost:8083/api/v1/cluster-info | jq '{nodeCount, totalGPUs, healthy, lastSyncAt}'

# View unhealthy nodes
kubectl exec -n kube-system $POD_NAME -- curl -s http://localhost:8083/api/v1/cluster-info | jq '.nodes[] | select(.healthy == false)'

# View Pending Placements
kubectl exec -n kube-system $POD_NAME -- curl -s http://localhost:8083/api/v1/placements | jq '.placements[] | select(.phase == "Pending")'

# Count Placements by phase
kubectl exec -n kube-system $POD_NAME -- curl -s http://localhost:8083/api/v1/placements | jq '[.placements[].phase] | group_by(.) | map({phase: .[0], count: length})'
```

---

## FAQ

### Q1: How are TLS certificates generated?

This project uses **kube-webhook-certgen** to auto-generate certificates. **No manual operation is required.**

**How it works:**
- A Kubernetes Job is automatically triggered during Helm install/upgrade
- The Job uses the `liangjw/kube-webhook-certgen:v1.1.1` image
- It automatically: creates CA → issues server certificate → creates Secret → patches Webhook caBundle

**Configuration location**: [values.yaml:67-80](chart/values.yaml#L67-L80)
```yaml
patch:
  enabled: true                # Enable auto certificate generation
  image:
    repository: liangjw/kube-webhook-certgen
    tag: v1.1.1
```

**Verify certificate was generated:**
```bash
kubectl get jobs -n kube-system | grep create-secret
kubectl get secret gpu-descheduler-webhook-tls -n kube-system
```

### Q2: Certificate expired or needs regeneration?

**The certgen solution handles certificate lifecycle automatically. Manual intervention is typically unnecessary.**

If you do need to force certificate regeneration:

```bash
# Method 1: Delete Secret and re-run Helm upgrade (recommended)
kubectl delete secret gpu-descheduler-webhook-tls -n kube-system
helm upgrade gpu-descheduler ./chart \
  --namespace kube-system \
  --timeout 5m

# Method 2: Restart the certgen Job directly
kubectl delete job gpu-descheduler-webhook-create-secret -n kube-system
helm upgrade gpu-descheduler ./chart \
  --namespace kube-system \
  --timeout 5m

# Restart Webhook Pod to load the new certificate
kubectl rollout restart deployment/gpu-descheduler-webhook -n kube-system
```

> **Note**: Certgen-generated certificates have a long validity period (by default configuration). Rotation is normally not needed.

### Q3: How to Clean Up and Uninstall

The project provides an **automated uninstall script**. **Using the script for one-click complete cleanup is recommended.**

#### Method 1: Full Uninstall Script (Recommended)

[uninstall-gpu-descheduler.sh](chart/uninstall-gpu-descheduler.sh) provides a **6-step complete cleanup process**:

```bash
cd chart

# One-click full uninstall (automatically performs all steps below)
./uninstall-gpu-descheduler.sh

# The script automatically:
# ✅ 1. Uninstalls Helm Release (helm uninstall)
# ✅ 2. Deletes TLS Secret
# ✅ 3. Deletes CRD Definition
# ✅ 4. Deletes Leader Election Lease
# ✅ 5. Cleans up residual Pods
# ✅ 6. Verifies cleanup result
```

**Script Features:**
- 🔄 **Idempotent**: Can be run repeatedly; already-deleted resources are automatically skipped
- 🔍 **Verification**: Automatically checks for residual resources after execution
- 📊 **Detailed Logging**: Progress indicators and status feedback for each step
- ⚠️ **Safety Prompts**: Shows resource types to be deleted before cleanup

#### Method 2: Quick Cleanup Script

[cleanup.sh](chart/cleanup.sh) provides a **5-step quick cleanup** (more aggressive):

```bash
cd chart

# Quick cleanup (force delete, no graceful termination)
./cleanup.sh

# Script features:
# ⚡ Force termination (--force --grace-period=0)
# 🧹 Also cleans up Jobs and Webhook Configuration
# 📦 Includes CA Secret cleanup
```

#### Method 3: Manual Commands (Not Recommended)

If you need to manually execute specific steps:

```bash
# Uninstall Helm Release only (keep other resources for debugging)
helm uninstall gpu-descheduler -n kube-system --timeout 2m

# Delete CRD only (keep deployment; recreating Placements will fail)
kubectl delete crd hamipodplacements.scheduling.hami.placement.com

# Clean up Secrets only (triggers certgen to regenerate certificates)
kubectl delete secret gpu-descheduler-webhook-tls -n kube-system
```

> **💡 Method 1 (uninstall-gpu-descheduler.sh) is recommended** — it's the safest and most complete cleanup solution.

---

## Advanced Configuration

### Custom Sync Interval

Modify in `chart/values.yaml`:

```yaml
args:
  - --sync-interval=30s    # More frequent sync (default 60s)
  # or
  - --sync-interval=5m     # Less frequent sync to reduce overhead
```

### Adjust Log Level

```yaml
args:
  - --v=0    # Info level (recommended for production)
  - --v=3    # Debug level (for debugging)
  - --v=5    # Trace level (detailed tracing)
```

> **⚠️ Important**: This Webhook **only supports single-instance deployment**. **Multi-replica / Leader Election is not supported.**
>
> Reasons:
> - The Webhook needs to maintain Placement CRD cache state
> - Multiple instances would cause cache inconsistency and duplicate processing
> - Set `replicaCount: 1`

---

## Monitoring and Alerting Recommendations

### Key Metric Monitoring

```bash
# Periodically check cluster health (can be added to cron or monitoring system)
*/5 * * * * kubectl exec -n kube-system $POD_NAME -- curl -s http://localhost:8083/api/v1/cluster-info | jq '.healthy' | grep -q false && echo "ALERT: Cluster unhealthy!"
```

### Log Monitoring Keywords

```bash
# Monitor key events
kubectl logs -f -n kube-system -l app.kubernetes/name=gpu-descheduler-webhook | grep -E "(ERROR|WEBHOOK_TRIGGERED|PLACEMENT_SELECTED|CLUSTER_STATE_CHECK)"
```

---

## Related Resources

- **Project Code**: [HAMi](https://github.com/Project-HAMi/HAMi)
- **HAMi Scheduler Documentation**: [Official Docs](https://github.com/Project-HAMi/HAMi/wiki)
- **Helm Best Practices**: [helm.sh/docs](https://helm.sh/docs/topics/best_practices/)

---

## Changelog

### v0.0.2 (2026-08-28)

- ✅ Helm one-click deployment support
- ✅ Cluster state REST API (`/api/v1/cluster-info`)
- ✅ Placement cache REST API (`/api/v1/placements`)
- ✅ **kube-webhook-certgen** auto TLS certificate generation (no manual operation or cert-manager needed)
- ✅ Single-instance deployment mode for cache state consistency
- ✅ ReqID idempotency guarantee mechanism

---

**Document Maintainer**: Vesta-mlp
**Last Updated**: 2026-08-28