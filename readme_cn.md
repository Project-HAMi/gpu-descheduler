# HAMi GPU Descheduler Webhook

## 项目简介

**HAMi GPU Descheduler** 是基于 **HAMi 虚拟化环境** 的专用 GPU 碎片整理工具，通过 **Mutating Admission Webhook** 和 **自定义资源（CRD）** 实现对 HAMi 虚拟化节点上 GPU 作业的精细化调度控制。

> **⚠️ 本项目 **必须基于 HAMi 虚拟化环境运行**，**不能脱离 HAMi Scheduler 单独部署使用**
>
> - ✅ 适用范围：仅针对 **HAMi 虚拟化节点** 及其上的 **vGPU Pod**
> - ❌ 不适用场景：原生 Kubernetes GPU 节点、非 HAMi 管理的工作负载
> - 🔗 依赖关系：必须与 [HAMi Scheduler](https://github.com/Project-HAMi/HAMi) 配合使用
>
> **工作原理**:
> 1. 从 HAMi Scheduler 获取集群 GPU 虚拟化状态
> 2. 监控 HAMi vGPU Pod 的调度和分配情况
> 3. 通过 Placement CRD 实现跨节点的 GPU 碎片整理
> 4. 仅处理通过 HAMi Scheduler 调度的 GPU 工作负载

### 核心功能

- ✅ **GPU 碎片整理**：将分散在不同节点的 GPU 资源整合，提升集群利用率
- ✅ **精确绑定**：指定 Pod 调度到特定节点和 GPU 设备
- ✅ **主动驱逐**：支持驱逐现有 Pod 并重新调度到目标节点
- ✅ **幂等性保证**：通过 ReqID 确保操作的可追溯性和防重复执行
- ✅ **自动发现**：自动发现 HAMi Scheduler 的 Pod IP，无需手动配置
- ✅ **状态监控**：提供 REST API 查询集群状态和分配记录

---

## 环境要求

### 前置依赖（必须满足）

| 组件 | 版本/状态要求 | 说明 |
|------|--------------|------|
| **HAMi Scheduler** | ✅ **已部署并正常运行** | 核心依赖，**必须先于本项目部署** |
| **HAMi 虚拟化节点** | ✅ 至少 1 个节点已启用 HAMi vGPU | 目标管理对象 |
| Kubernetes | v1.24+ (推荐 v1.28+) | 基础运行环境 |
| Helm | v3.8+ | 部署工具 |
| Docker | 用于构建镜像 | 构建时需要 |

> **⚠️ 关键前提**: 在部署本项目之前，请确保：
>
> 1. **HAMi Scheduler 已成功部署**: `kubectl get pods -A | grep hami-scheduler` 应显示 Running
> 2. **节点已启用 HAMi 虚拟化**: 节点上应存在 HAMi Device Plugin (`kubectl get pods -A | grep hami-device-plugin`)
> 3. **HAMi Scheduler Metrics 可访问**: 默认端口 `9395`，用于获取集群 GPU 状态
>
> **如果以上条件不满足，请先完成 HAMi 环境搭建**:
> - 📘 [HAMi 官方文档](https://github.com/Project-HAMi/HAMi/wiki)
> - 🚀 [HAMi 快速部署指南](https://github.com/Project-HAMi/HAMi/blob/main/docs/zh-CN/installation.md)

### 本项目自动管理的组件

| 组件 | 说明 | 是否需要手动安装 |
|------|------|-----------------|
| TLS 证书 | 使用 kube-webhook-certgen 自动生成 | ❌ 不需要（自动） |
| CRD 定义 | HamiPodPlacement 资源定义 | ❌ 不需要（自动） |
| RBAC 权限 | Webhook 所需的 ClusterRole/SA | ❌ 不需要（自动） |
| MutatingWebhook | 准入控制器注册 | ❌ 不需要（自动） |

> **✅ 注意**: 除上表中的"前置依赖"外，其他所有组件均由 Helm Chart **自动创建和管理**，无需手动操作

---

## 文档

- 📘 [完整部署指南（英文）](introduction.md)
- 📗 [完整部署指南（中文）](introduction_cn.md) — 构建、部署、验证、FAQ 及高级配置

---

## 相关资源链接

- **项目代码**: [HAMi](https://github.com/Project-HAMi/HAMi)
- **HAMi Scheduler 文档**: [官方文档](https://github.com/Project-HAMi/HAMi/wiki)