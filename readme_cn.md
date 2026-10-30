# HAMi GPU Descheduler Webhook

## 项目简介

**HAMi GPU Descheduler** 是面向 **HAMi** 的专用 GPU 碎片整理工具。它通过 **Mutating Admission Webhook** 和 **自定义资源（CRD）**，对 HAMi 虚拟化节点上的 GPU 作业做精细化调度控制。功能包括：

- **GPU 碎片整理**：将分散在不同节点的 GPU 资源整合，提升集群利用率
- **精确绑定**：将 Pod 调度到指定节点和 GPU 设备
- **主动驱逐**：驱逐现有 Pod，并重新调度到目标节点


## 环境要求

### 前置依赖

| 组件 | 版本/状态 |
|------|----------|
| **HAMi Scheduler** | v2.10.0+ |
| Kubernetes | v1.24+（推荐 v1.28+） |
| Helm | v3.8+ |


## 文档

- 📘 [English](readme.md)

## 构建镜像

项目提供了自动化构建脚本，**推荐使用脚本构建**而非手动 `docker build`：

```bash
./build-image.sh
```

详细说明参见：[build-image.sh](build-image.sh)

## 部署

### 使用 helm install 部署

```bash
helm install gpu-descheduler . --namespace kube-system --create-namespace --debug
```

**参数说明：**
| 参数 | 说明 |
|------|------|
| `--namespace` | 部署目标命名空间 |
| `--create-namespace` | 自动创建命名空间（如果不存在） |
| `--debug` | 显示详细的调试信息 |
| `--timeout 5m` | 设置超时时间为 5 分钟 |
| `-f values.yaml` | 使用自定义配置文件（可选） |


## 验证部署

```bash
kubectl get pods -n kube-system -l app.kubernetes/name=gpu-descheduler-webhook -w

# 预期输出：
NAME                                      READY   STATUS    RESTARTS   AGE
gpu-descheduler-webhook-5f7d9c6b4d-abc12  1/1     Running   0          60s
gpu-descheduler-webhook-5f7d9c6b4d-def34  1/1     Running   0          60s
```

## 使用 Curl 查看状态

Webhook 服务提供 **两个核心 REST API 端点**，监听在容器内的 **8083** 端口：

| 端点路径 | 用途 | 返回信息 |
|---------|------|---------|
| `/api/v1/cluster-info` | 集群状态信息 | 节点列表、GPU 设备、健康状态、最后同步时间 |
| `/api/v1/placements` | Placement CRD 缓存 | 分配记录、消费状态、匹配的 Pod 信息 |


```bash
# 在容器内执行
curl -s http://localhost:8083/api/v1/cluster-info | jq .
```

## 使用

你需要创建CRD来进行使用。CRD 清单在 `chart/templates/crd/hamipodplacement.yaml`。`helm install` 会随 chart 一起装上。字段定义就维护在这个文件里，仓库里没有用 `controller-gen` 生成它的步骤。

只安装 CRD：

```bash
kubectl apply -f chart/templates/crd/hamipodplacement.yaml
kubectl get crd hamipodplacements.scheduling.hami.placement.com
```

一份 `HamiPodPlacement` 只会被一个 Pod 消费。Pod 的 `schedulerName` 必须是 `hami-scheduler`，并且要匹配 `podSelector`：

```yaml
apiVersion: scheduling.hami.placement.com/v1
kind: HamiPodPlacement
metadata:
  name: pin-gpu0
  namespace: default
spec:
  reqID: "pin-gpu0-001"          # 最短 5 个字符，只消费一次
  podSelector:
    matchLabels:
      app: my-job
  nodeName: gpu-node-1
  gpuDevices:
    - gpu-0                      # 或 gpu-0/70
  evictPod: my-job-6cd5964877-zdb5z   # 同一命名空间里的 Pod 名
```

```bash
kubectl get hamipodplacements -A
```
