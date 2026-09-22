# HAMi GPU Descheduler Webhook

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

## Documentation

- 📘 [Complete Deployment Guide](introduction.md) — Building, deploying, verifying, FAQ, and advanced configuration
- 📗 [完整部署指南（中文）](introduction_cn.md)

---

## Related Resources

- **Project Code**: [HAMi](https://github.com/Project-HAMi/HAMi)
- **HAMi Scheduler Documentation**: [Official Docs](https://github.com/Project-HAMi/HAMi/wiki)