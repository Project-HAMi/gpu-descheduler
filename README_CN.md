# HAMi GPU Descheduler

[![CI](https://github.com/Project-HAMi/gpu-descheduler/actions/workflows/ci.yml/badge.svg)](https://github.com/Project-HAMi/gpu-descheduler/actions/workflows/ci.yml)
[![OpenSSF Scorecard](https://api.scorecard.dev/projects/github.com/Project-HAMi/gpu-descheduler/badge)](https://scorecard.dev/viewer/?uri=github.com/Project-HAMi/gpu-descheduler)
[![License](https://img.shields.io/badge/license-Apache%202.0-blue.svg)](LICENSE)

[English](README.md)

descheduler 用来在 [HAMi](https://github.com/Project-HAMi/HAMi) 集群里迁移 GPU Pod。你用 `HamiPodPlacement` 指定 Pod 去哪里。它可以驱逐旧 Pod，并把下一个匹配的 Pod 固定到你选的 GPU 上。

它目前不会自己挑选要迁移的 Pod。见 [#36](https://github.com/Project-HAMi/gpu-descheduler/issues/36)。

## 环境要求

| 组件 | 版本 |
|------|------|
| HAMi | v2.10.0 及以上。已在 v2.10.0 和 v2.11.0 上测试。 |
| Kubernetes | v1.24 及以上。已在 v1.24 和 v1.36 上测试。 |
| Helm | v3.8 及以上 |

## 安装

安装发布版本：

```bash
helm install gpu-descheduler oci://docker.io/projecthami/gpu-descheduler-webhook \
  --version 0.0.2-chart --namespace kube-system
```

chart 和镜像放在 Docker Hub 的同一个仓库，标签是 `<版本>-chart`。v0.0.2 之后的镜像用 cosign 签名，并附带 SBOM。

从源码安装：

```bash
helm install gpu-descheduler ./chart --namespace kube-system --create-namespace
```

所有资源都装在 `--namespace` 指定的命名空间里。

检查是否运行：

```bash
kubectl get pods -n kube-system -l app=gpu-descheduler-webhook
```

卸载（包括 CRD 和 TLS Secret）：

```bash
NAMESPACE=kube-system ./chart/uninstall-gpu-descheduler.sh
```

## 构建

用 `./build-image.sh` 构建镜像，`./build-image.sh -h` 查看参数。编译二进制需要 `go.mod` 里的 Go 版本。

## 连接 HAMi

descheduler 从 HAMi 调度器的 metrics 读取 GPU 用量，通过 HAMi 调度器的 Service 找到它。如果 HAMi 用了别的名称或命名空间，在 chart 的 `args` 里设置：

| 参数 | 默认值 | 含义 |
|------|--------|------|
| `--scheduler-namespace` | `kube-system` | HAMi 调度器 Service 的命名空间 |
| `--scheduler-service` | `hami-scheduler` | HAMi 调度器 Service 的名称 |
| `--scheduler-port` | `9395` | HAMi 调度器容器的 metrics 端口 |
| `--sync-interval` | `1m` | 读取 GPU 用量的间隔 |

## 使用

CRD 在 `chart/templates/crd/hamipodplacement.yaml`，chart 会自动安装。单独安装：

```bash
kubectl apply -f chart/templates/crd/hamipodplacement.yaml
```

一个 placement 只给一个 Pod 用。这个 Pod 必须使用 `schedulerName: hami-scheduler`，并匹配 `podSelector`。

```yaml
apiVersion: scheduling.hami.placement.com/v1
kind: HamiPodPlacement
metadata:
  name: pin-gpu0
  namespace: default
spec:
  reqID: "pin-gpu0-001"          # 至少 5 个字符，只用一次
  podSelector:
    matchLabels:
      app: my-job
  nodeName: gpu-node-1
  gpuDevices:
    - gpu-0                      # 或 gpu-0/70
  evictPod: my-job-6cd5964877-zdb5z   # 可选，同一命名空间里的 Pod
```

```bash
kubectl get hamipodplacements -A
```

更多示例见 [deploy/yaml/pod-placement-demo.yaml](deploy/yaml/pod-placement-demo.yaml)。

### 驱逐

驱逐 `evictPod` 之前，控制器会检查目标 GPU。GPU 必须存在，并且放得下这个 Pod。否则 placement 失败，Pod 继续运行。

释放出来的容量不会预留。新 Pod 调度之前，别的 Pod 可能先占用。见 [#10](https://github.com/Project-HAMi/gpu-descheduler/issues/10)。

### 阶段

| 阶段 | 含义 |
|------|------|
| `EvictingOldPod` | 正在驱逐 `evictPod` 中的 Pod |
| `Pending` | 目标已检查，等待匹配的 Pod |
| `Matched` | 匹配的 Pod 已用这个 placement 创建 |
| `Completed` | 匹配的 Pod 正在运行 |
| `Failed` | 出错了，原因见 `message` |

## 调试

descheduler 在 8083 端口提供状态：

```bash
kubectl -n kube-system port-forward deploy/gpu-descheduler-webhook 8083 &
curl -s localhost:8083/api/v1/cluster-info   # 节点和 GPU
curl -s localhost:8083/api/v1/placements     # placement
```

其他端口：webhook 8443，metrics 8080，健康检查 8081。
