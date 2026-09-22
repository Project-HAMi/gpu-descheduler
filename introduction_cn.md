# HAMi GPU Descheduler Webhook 完整部署指南

## 目录

1. [制作 Helm 包](#制作-helm-包)
2. [一键式部署](#一键式部署)
3. [验证部署](#验证部署)
4. [使用 Curl 查看状态](#使用-curl-查看状态)
5. [常见问题](#常见问题)
6. [高级配置](#高级配置)
7. [监控与告警建议](#监控与告警建议)

---

## 制作 Helm 包

### 步骤 1: 构建镜像（使用 build-image.sh 脚本）

项目提供了自动化构建脚本，**推荐使用脚本构建**而非手动 docker build：

```bash
cd /path/to/gpu-descheduler

# 基本用法 - 使用默认配置构建
./build-image.sh

# 指定版本标签
./build-image.sh -t v0.0.2

# 构建并推送到仓库
./build-image.sh -t v0.0.2 --push

# 多架构构建（amd64 + arm64）
./build-image.sh -t latest -p all --push

# 查看帮助信息
./build-image.sh -h
```

**脚本功能特性：**
- ✅ 自动注入 Git 版本信息和构建时间
- ✅ 支持多架构构建 (linux/amd64, linux/arm64)
- ✅ 自动推送到 Harbor 镜像仓库
- ✅ 支持缓存控制 (`--no-cache`)
- ✅ 详细的构建日志输出

**常用参数说明：**
```bash
选项:
  -t, --tag <tag>          镜像标签 (默认: v0.0.2)
  -r, --registry <url>     镜像仓库地址前缀
  -p, --platform <arch>    目标平台 (linux/amd64, linux/arm64, all)
  -v, --version <ver>      注入到二进制的版本号
  --push                   构建后自动推送
  --no-cache               不使用 Docker 缓存
  -h, --help               显示帮助信息
```

详细说明参见：[build-image.sh](build-image.sh)

### 步骤 2: 打包 Helm Chart

```bash
cd chart

# 查看当前 chart 结构
tree .
# 输出：
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

# 打包 Helm Chart（生成 .tgz 文件）
helm package .

# 输出：gpu-descheduler-webhook-0.1.0.tgz
```

### 步骤 3: （可选）推送到 Helm 仓库

```bash
# 如果有私有 Helm 仓库
helm push gpu-descheduler-webhook-0.1.0.tgz http://your-helm-repo/

# 或者直接使用本地文件进行部署
```

---

## 一键式部署

### 前置检查

```bash
# 1. 检查 K8s 集群连接
kubectl cluster-info

# 2. 检查 HAMi Scheduler 是否运行
kubectl get pods -A | grep hami-scheduler
kubectl get svc -A | grep hami-scheduler
```

### 配置部署参数

> **⚠️ 重要**: 所有部署参数统一在 [chart/values.yaml](chart/values.yaml) 中维护，**无需单独创建 deploy-values.yaml 文件**。

**如需自定义配置，直接修改 `chart/values.yaml` 即可：**

```bash
cd chart

# 查看默认配置
cat values.yaml | head -50

# 根据实际环境修改以下关键参数：
#   - image.repository / image.tag        # 镜像地址和版本
#   - namespace                             # 部署命名空间
#   - replicaCount                          # 副本数（⚠️ 必须设置为 1，不支持多实例）
#   - resources.requests/limits             # 资源限制
#   - args                                  # 容器启动参数
#   - patch.enabled                         # certgen 自动证书生成开关
```

**常用自定义场景：**

| 场景 | 修改参数 |
|------|---------|
| 使用私有镜像仓库 | `image.repository`, `image.tag`, `image.pullSecrets` |
| 调整资源配额 | `resources.requests.cpu/memory`, `resources.limits.cpu/memory` |
| 修改日志级别 | 在 `args` 中调整 `-v=3` (0-5) |
| 禁用自动证书生成 | `patch.enabled: false` |
| 自定义同步频率 | 在 `args` 中调整 `--sync-interval=60s` |

> **提示**: 修改完成后无需额外步骤，直接执行下方的安装命令即可生效

### 执行一键部署

```bash
cd chart

# 使用项目标准安装命令（推荐）
helm install gpu-descheduler . --namespace kube-system --create-namespace --debug

# 如果使用自定义配置文件（可选）
helm install gpu-descheduler . \
  -f values.yaml \
  --namespace kube-system \
  --create-namespace \
  --debug

# 使用打包后的 tgz 文件部署
# （如何打包见上方"步骤 2: 打包 Helm Chart"）
helm install gpu-descheduler ./gpu-descheduler-webhook-0.1.0.tgz \
  --namespace kube-system \
  --create-namespace \
  --debug \
  --timeout 5m
```

**参数说明：**
| 参数 | 说明 |
|------|------|
| `--namespace` | 部署目标命名空间 |
| `--create-namespace` | 自动创建命名空间（如果不存在） |
| `--debug` | 显示详细的调试信息 |
| `--timeout 5m` | 设置超时时间为 5 分钟 |
| `-f values.yaml` | 使用自定义配置文件（可选） |

> **提示**: 建议始终使用 `--timeout 5m` 参数，避免因网络问题导致长时间等待

### 部署成功标志

```bash
# 看到以下输出表示部署成功：
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

## 验证部署

### 1. 检查 Pod 状态

```bash
kubectl get pods -n kube-system -l app.kubernetes/name=gpu-descheduler-webhook -w

# 预期输出：
NAME                                      READY   STATUS    RESTARTS   AGE
gpu-descheduler-webhook-5f7d9c6b4d-abc12  1/1     Running   0          60s
gpu-descheduler-webhook-5f7d9c6b4d-def34  1/1     Running   0          60s
```

### 2. 检查 Webhook 注册

```bash
kubectl get mutatingwebhookconfiguration | grep defrag

# 预期输出：
gpu-descheduler-mutating-webhook   2026-08-28T10:30:00Z   1
```

### 3. 检查 TLS 证书自动生成

```bash
# 查看 certgen 创建的 TLS Secret（由 Helm 自动生成）
kubectl get secret gpu-descheduler-webhook-tls -n kube-system

# 预期输出：
NAME                          TYPE                DATA   AGE
gpu-descheduler-webhook-tls   kubernetes.io/tls   2      10m

# 查看 certgen Job 执行状态（应该显示 Completed）
kubectl get jobs -n kube-system | grep create-secret

# 预期输出：
gpu-descheduler-webhook-create-secret   1/1           10m
```

> **说明**: TLS 证书由 **kube-webhook-certgen** 在 `helm install` 时**自动生成**，无需手动操作

### 4. 检查服务端点

```bash
kubectl get endpoints gpu-descheduler-webhook -n kube-system

# 预期输出：
NAME                         ENDPOINTS                           AGE
gpu-descheduler-webhook      10.244.x.x:8443,10.244.y.y:8443   1m
```

### 5. 查看 Pod 日志

```bash
# 查看 webhook 启动日志
kubectl logs -f -n kube-system -l app.kubernetes/name=gpu-descheduler-webhook

# 应该看到类似输出：
# 1. "Registered HTTP endpoint: GET /api/v1/cluster-info"
# 2. "Registered HTTP endpoint: GET /api/v1/placements"
# 3. "Starting API server (cluster-info + placements)" addr=":8083"
# 4. "starting gpu-descheduler manager: controller + mutate webhook"
```

---

## 使用 Curl 查看状态

Webhook 服务提供了 **两个核心 REST API 端点**，监听在容器内的 **8083 端口**：

| 端点路径 | 用途 | 返回信息 |
|---------|------|---------|
| `/api/v1/cluster-info` | 集群状态信息 | 节点列表、GPU设备、健康状态、最后同步时间 |
| `/api/v1/placements` | Placement CRD 缓存 | 分配记录、消费状态、匹配的 Pod 信息 |

### 方式一：进入容器执行（推荐）

这是最直接的方式，无需端口转发。

#### 步骤 1: 获取 Pod 名称

```bash
kubectl get pods -n kube-system -l app.kubernetes/name=gpu-descheduler-webhook

# 输出示例：
NAME                                      READY   STATUS    RESTARTS   AGE
gpu-descheduler-webhook-5f7d9c6b4d-abc12  1/1     Running   0          10m
```

#### 步骤 2: 进入容器

```bash
# 使用上一步获取的 Pod 名称
kubectl exec -it -n kube-system gpu-descheduler-webhook-5f7d9c6b4d-abc12 -- /bin/bash

# 如果容器没有 bash，尝试 sh
kubectl exec -it -n kube-system gpu-descheduler-webhook-5f7d9c6b4d-abc12 -- /bin/sh
```

#### 步骤 3: 查看集群信息

```bash
# 在容器内执行
curl -s http://localhost:8083/api/v1/cluster-info | jq .
```

**字段说明：**

| 字段 | 类型 | 说明 |
|------|------|------|
| `nodeCount` | int | 集群中节点总数 |
| `totalGPUs` | int | GPU 设备总数 |
| `healthy` | bool | 整体健康状态（所有节点都健康时为 true） |
| `lastSyncAt` | string | 最后一次从 Scheduler 同步数据的时间 |
| `syncError` | string | 同步错误信息（无错误时为空） |
| `nodes[].nodeName` | string | 节点名称 |
| `nodes[].healthy` | bool | 节点健康状态 |
| `nodes[].gpuCount` | int | 该节点上的 GPU 数量 |
| `nodes[].devices[]` | array | GPU 设备详情列表 |
| `devices[].id` | string | 设备 ID |
| `devices[].uuid` | string | GPU UUID（唯一标识符） |
| `devices[].type` | string | GPU 型号（如 A100-80G） |
| `devices[].memoryMB` | int32 | 显存大小（MB） |
| `devices[].health` | bool | 设备健康状态 |

#### 步骤 4: 查看 Placement 缓存信息

```bash
# 在容器内执行
curl -s http://localhost:8083/api/v1/placements | jq .
```

**字段说明：**

| 字段 | 类型 | 说明 |
|------|------|------|
| `totalCount` | int | 缓存中的 Placement 总数 |
| `placements[].namespace` | string | Placement 所在命名空间 |
| `placements[].name` | string | Placement CRD 名称 |
| `placements[].reqID` | string | 请求 ID（用于幂等性保证） |
| `placements[].nodeName` | string | 目标节点名称 |
| `placements[].gpuDevices[]` | array | 分配的 GPU 设备索引列表 |
| `placements[].phase` | string | 状态阶段（Pending/Consumed/Failed） |
| `placements[].message` | string | 状态描述信息 |
| `placements[].createdAt` | string | 创建时间 |
| `placements[].consumedAt` | string | 被消费的时间（未消费时为 null） |
| `placements[].matchedPodName` | string | 匹配到的 Pod 名称（未匹配时为 null） |
| `placements[].assignments[]` | array | 具体的 Pod-GPU 分配记录 |

#### 步骤 5: 退出容器

```bash
exit
```

### 方式二：使用 kubectl 端口转发

如果你不想进入容器，可以使用端口转发从本地访问。

```bash
# 启动端口转发（将容器的 8083 端口映射到本地）
kubectl port-forward svc/gpu-descheduler-webhook 8083:8083 -n kube-system &

# 在另一个终端窗口执行
curl -s http://localhost:8083/api/v1/cluster-info | jq .
curl -s http://localhost:8083/api/v1/placements | jq .

# 停止端口转发
kill %1  # 或 Ctrl+C
```

### 方式三：单次命令执行（无需交互）

```bash
# 单次查询集群信息
kubectl exec -n kube-system $(kubectl get pod -n kube-system -l app.kubernetes/name=gpu-descheduler-webhook -o jsonpath='{.items[0].metadata.name}') -- curl -s http://localhost:8083/api/v1/cluster-info | jq .

# 单次查询 Placement 缓存
kubectl exec -n kube-system $(kubectl get pod -n kube-system -l app.kubernetes/name=gpu-descheduler-webhook -o jsonpath='{.items[0].metadata.name}') -- curl -s http://localhost:8083/api/v1/placements | jq .
```

### 快速调试命令汇总

```bash
# 设置变量（简化后续命令）
POD_NAME=$(kubectl get pod -n kube-system -l app.kubernetes/name=gpu-descheduler-webhook -o jsonpath='{.items[0].metadata.name}')

# 查看集群概要（仅显示关键信息）
kubectl exec -n kube-system $POD_NAME -- curl -s http://localhost:8083/api/v1/cluster-info | jq '{nodeCount, totalGPUs, healthy, lastSyncAt}'

# 查看不健康的节点
kubectl exec -n kube-system $POD_NAME -- curl -s http://localhost:8083/api/v1/cluster-info | jq '.nodes[] | select(.healthy == false)'

# 查看 Pending 状态的 Placement
kubectl exec -n kube-system $POD_NAME -- curl -s http://localhost:8083/api/v1/placements | jq '.placements[] | select(.phase == "Pending")'

# 统计各状态的 Placement 数量
kubectl exec -n kube-system $POD_NAME -- curl -s http://localhost:8083/api/v1/placements | jq '[.placements[].phase] | group_by(.) | map({phase: .[0], count: length})'
```

---

## 常见问题

### Q1: TLS 证书如何生成？

本项目使用 **kube-webhook-certgen** 自动生成证书，**完全无需手动操作**。

**工作原理：**
- Helm install/upgrade 时自动触发 Kubernetes Job
- Job 使用 `liangjw/kube-webhook-certgen:v1.1.1` 镜像
- 自动完成：创建 CA → 签发服务器证书 → 创建 Secret → Patch Webhook caBundle

**配置位置**: [values.yaml:67-80](chart/values.yaml#L67-L80)
```yaml
patch:
  enabled: true                # 启用自动证书生成
  image:
    registry: "docker.io"
    repository: "liangjw/kube-webhook-certgen"
    tag: "v1.1.1"
```

**验证证书已生成：**
```bash
kubectl get jobs -n kube-system | grep create-secret
kubectl get secret gpu-descheduler-webhook-tls -n kube-system
```

### Q2: 证书过期或需要重新生成？

**certgen 方案自动处理证书生命周期，通常无需手动干预。**

如果确实需要强制重新生成证书：

```bash
# 方法一：删除 Secret 后重新运行 Helm upgrade（推荐）
kubectl delete secret gpu-descheduler-webhook-tls -n kube-system
helm upgrade gpu-descheduler ./chart \
  --namespace kube-system \
  --timeout 5m

# 方法二：直接重启 certgen Job
kubectl delete job gpu-descheduler-webhook-create-secret -n kube-system
helm upgrade gpu-descheduler ./chart \
  --namespace kube-system \
  --timeout 5m

# 重启 Webhook Pod 以加载新证书
kubectl rollout restart deployment/gpu-descheduler-webhook -n kube-system
```

> **注意**: certgen 生成的证书有效期很长（默认配置），正常情况下不需要轮换

### Q3: 如何清理和卸载

项目提供了**自动化卸载脚本**，**推荐使用脚本一键完成所有清理操作**。

#### 方式一：使用完整卸载脚本（推荐）

[uninstall-gpu-descheduler.sh](chart/uninstall-gpu-descheduler.sh) 提供 **6 步完整清理流程**：

```bash
cd chart

# 一键执行完整卸载（自动完成以下所有步骤）
./uninstall-gpu-descheduler.sh

# 脚本自动执行：
# ✅ 1. 卸载 Helm Release (helm uninstall)
# ✅ 2. 删除 TLS Secret
# ✅ 3. 删除 CRD 定义
# ✅ 4. 删除 Leader Election Lease
# ✅ 5. 清理残留 Pod
# ✅ 6. 验证清理结果
```

**脚本特性：**
- 🔄 **幂等性**: 可重复执行，已删除的资源会自动跳过
- 🔍 **验证机制**: 执行后自动检查是否有残留资源
- 📊 **详细日志**: 每步都有进度提示和状态反馈
- ⚠️ **安全提示**: 清理前会显示将要删除的资源类型

#### 方式二：使用快速清理脚本

[cleanup.sh](chart/cleanup.sh) 提供 **5 步快速清理**（更激进）：

```bash
cd chart

# 快速清理（强制删除，不等待优雅终止）
./cleanup.sh

# 脚本特点：
# ⚡ 强制终止 (--force --grace-period=0)
# 🧹 同时清理 Jobs 和 Webhook Configuration
# 📦 包含 CA Secret 清理
```

#### 方式三：手动命令（不推荐）

如果需要手动执行某些特定步骤：

```bash
# 仅卸载 Helm Release（保留其他资源用于调试）
helm uninstall gpu-descheduler -n kube-system --timeout 2m

# 仅删除 CRD（保留部署，重新创建 Placement 会失败）
kubectl delete crd hamipodplacements.scheduling.hami.placement.com

# 仅清理 Secrets（触发 certgen 重新生成证书）
kubectl delete secret gpu-descheduler-webhook-tls -n kube-system
```

> **💡 推荐使用方式一（uninstall-gpu-descheduler.sh）**，它是最安全、最完整的清理方案

---

## 高级配置

### 自定义同步间隔

在 `chart/values.yaml` 中修改：

```yaml
args:
  - --sync-interval=30s    # 更频繁地同步（默认 60s）
  # 或
  - --sync-interval=5m     # 降低同步频率以减少开销
```

### 调整日志级别

```yaml
args:
  - --v=0    # Info 级别（生产推荐）
  - --v=3    # Debug 级别（调试用）
  - --v=5    # Trace 级别（详细追踪）
```

> **⚠️ 重要**: 本 Webhook **仅支持单实例部署**，**不支持多副本/Leader Election**
>
> 原因：
> - Webhook 需要维护 Placement CRD 缓存状态
> - 多实例会导致缓存不一致和重复处理
> - 建议设置 `replicaCount: 1`

---

## 监控与告警建议

### 关键指标监控

```bash
# 定期检查集群健康状态（可加入 cron 或监控系统）
*/5 * * * * kubectl exec -n kube-system $POD_NAME -- curl -s http://localhost:8083/api/v1/cluster-info | jq '.healthy' | grep -q false && echo "ALERT: Cluster unhealthy!"
```

### 日志监控关键词

```bash
# 监控关键事件
kubectl logs -f -n kube-system -l app.kubernetes/name=gpu-descheduler-webhook | grep -E "(ERROR|WEBHOOK_TRIGGERED|PLACEMENT_SELECTED|CLUSTER_STATE_CHECK)"
```

---

## 更新日志

### v0.0.2 (2026-08-28)

- ✅ 支持 Helm 一键部署
- ✅ 提供集群状态 REST API (`/api/v1/cluster-info`)
- ✅ 提供 Placement 缓存 REST API (`/api/v1/placements`)
- ✅ **kube-webhook-certgen** 自动生成 TLS 证书（无需手动操作或 cert-manager）
- ✅ 单实例部署模式，确保缓存状态一致性
- ✅ ReqID 幂等性保证机制