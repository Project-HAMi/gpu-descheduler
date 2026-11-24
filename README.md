# HAMi GPU Descheduler

[![CI](https://github.com/Project-HAMi/gpu-descheduler/actions/workflows/ci.yml/badge.svg)](https://github.com/Project-HAMi/gpu-descheduler/actions/workflows/ci.yml)
[![OpenSSF Scorecard](https://api.scorecard.dev/projects/github.com/Project-HAMi/gpu-descheduler/badge)](https://scorecard.dev/viewer/?uri=github.com/Project-HAMi/gpu-descheduler)
[![License](https://img.shields.io/badge/license-Apache%202.0-blue.svg)](LICENSE)

English | [中文](README_CN.md)

The descheduler moves GPU Pods on [HAMi](https://github.com/Project-HAMi/HAMi) clusters. You tell it where a Pod should go with a `HamiPodPlacement`. It can evict the old Pod, and it pins the next matching Pod to the GPUs you chose.

## Requirements

| Component | Version |
|-----------|---------|
| HAMi | v2.10.0 or later. Tested with v2.10.0 and v2.11.0. |
| Kubernetes | v1.24 or later. Tested with v1.24 and v1.36. |
| Helm | v3.8 or later |

## Install

Install a release:

```bash
helm install gpu-descheduler oci://docker.io/projecthami/gpu-descheduler-webhook \
  --version 0.0.2-chart --namespace kube-system
```

The chart lives next to the image on Docker Hub, tagged `<version>-chart`. Images after v0.0.2 are signed with cosign and ship an SBOM. Later releases also attach the chart package and its cosign bundle to the GitHub release. To verify them:

```bash
ID='^https://github.com/Project-HAMi/gpu-descheduler/\.github/workflows/release\.yml@refs/tags/v'
ISSUER=https://token.actions.githubusercontent.com

cosign verify docker.io/projecthami/gpu-descheduler-webhook:<tag> \
  --certificate-identity-regexp "$ID" --certificate-oidc-issuer "$ISSUER"

cosign verify-blob gpu-descheduler-webhook-<version>-chart.tgz \
  --bundle gpu-descheduler-webhook-<version>-chart.tgz.sigstore.json \
  --certificate-identity-regexp "$ID" --certificate-oidc-issuer "$ISSUER"
```

Install from source:

```bash
helm install gpu-descheduler ./chart --namespace kube-system --create-namespace
```

Everything goes into the namespace you pass with `--namespace`.

Check that it runs:

```bash
kubectl get pods -n kube-system -l app=gpu-descheduler-webhook
```

To remove it, including the CRD and the TLS Secret:

```bash
NAMESPACE=kube-system ./chart/uninstall-gpu-descheduler.sh
```

## Build

Build the image with `./build-image.sh`. Run `./build-image.sh -h` for options. Building the binary needs the Go version from `go.mod`.

## Connecting to HAMi

The descheduler reads GPU usage from the HAMi scheduler metrics. It finds them through the HAMi scheduler Service. If HAMi runs under another name or namespace, set these in the chart `args`:

| Flag | Default | Meaning |
|------|---------|---------|
| `--scheduler-namespace` | `kube-system` | Namespace of the HAMi scheduler Service |
| `--scheduler-service` | `hami-scheduler` | Name of the HAMi scheduler Service |
| `--scheduler-port` | `9395` | Metrics port of the HAMi scheduler container |
| `--sync-interval` | `1m` | How often GPU usage is read |

## Usage

The CRD is in `chart/templates/crd/hamipodplacement.yaml`. The chart installs it. To install it alone:

```bash
kubectl apply -f chart/templates/crd/hamipodplacement.yaml
```

A placement is used by one Pod. That Pod must use `schedulerName: hami-scheduler` and match `podSelector`.

```yaml
apiVersion: scheduling.hami.placement.com/v1
kind: HamiPodPlacement
metadata:
  name: pin-gpu0
  namespace: default
spec:
  reqID: "pin-gpu0-001"          # at least 5 characters, used once
  podSelector:
    matchLabels:
      app: my-job
  nodeName: gpu-node-1
  gpuDevices:
    - gpu-0                      # or gpu-0/70
  evictPod: my-job-6cd5964877-zdb5z   # optional, Pod in the same namespace
```

```bash
kubectl get hamipodplacements -A
```

More examples are in [deploy/yaml/pod-placement-demo.yaml](deploy/yaml/pod-placement-demo.yaml).

### Eviction

Before it evicts `evictPod`, the controller checks the target GPUs. They must exist and have room for the Pod. If not, the placement fails and the Pod keeps running.

The freed capacity is not reserved. Another Pod can take it before the replacement is scheduled. See [#10](https://github.com/Project-HAMi/gpu-descheduler/issues/10).

### Phases

| Phase | Meaning |
|-------|---------|
| `EvictingOldPod` | Evicting the Pod in `evictPod` |
| `Pending` | Target checked, waiting for a matching Pod |
| `Matched` | A matching Pod was created with this placement |
| `Completed` | The matching Pod is running |
| `Failed` | Something went wrong. See `message`. |

## Automatic Defragmentation

With `defrag.enabled=true`, the descheduler plans the moves itself. On every run, for each HAMi pod that kube-scheduler reports as unschedulable, it looks for the fewest moves of other pods that free enough memory and cores on one GPU, and it reports the plan, plus a few ranked alternatives.

- **Only opted-in pods move.** A pod must carry the label `hami.io/defrag-opt-in: "true"` (set it in the workload's pod template). It must also be owned by a ReplicaSet (for example, through a Deployment), a StatefulSet or a Job, be running and ready, and use one GPU. Its PodDisruptionBudget must allow a disruption, and its workload must not have been moved within `defrag.cooldown`.
- **Pods are only moved for a waiting pod.** If nothing is unschedulable, nothing moves.
- **Dry run by default.** `defrag.mode=dryRun` only reports plans. `defrag.mode=execute` carries out one plan at a time, one move at a time: it checks the move against the current cluster, creates a `HamiPodPlacement` with `evictPod`, waits until the replacement pod runs on the planned GPU, and then waits for the unschedulable pod to be scheduled. If anything changes or fails, it stops.
- **Moves stay on their node** with `defrag.scope=sameNode`. Use `crossNode` to allow moves between nodes.

```bash
helm upgrade --install gpu-descheduler ./chart -n kube-system --set defrag.enabled=true
kubectl port-forward -n kube-system deploy/gpu-descheduler-webhook 8083:8083
curl -s http://localhost:8083/api/v1/defrag/plans | jq .    # latest plans
curl -s -X POST http://localhost:8083/api/v1/defrag/run      # run now
```

Set `defrag.defaultMemory`, `defrag.defaultCores` and `defrag.memoryFactor` to the HAMi scheduler's `nvidia.defaultMemory`, `nvidia.defaultCores` and `nvidia.memoryFactor`, so that requests are read the way HAMi reads them. Planning covers NVIDIA GPUs. Pods that use more than one GPU, MIG GPUs, or `nvidia.com/numa-bind` are left as they are. Metrics: `hami_defrag_pending_pods`, `hami_defrag_executions_total` and `hami_defrag_moves_total`.

A moved pod is restarted, not live-migrated. Between the eviction and the replacement pod's scheduling, another pod can take the space ([#10](https://github.com/Project-HAMi/gpu-descheduler/issues/10)). The plan then stops with the result `raced`, and that workload is not moved again during the cooldown.

## Debugging

The descheduler serves its state on port 8083:

```bash
kubectl -n kube-system port-forward deploy/gpu-descheduler-webhook 8083 &
curl -s localhost:8083/api/v1/cluster-info   # nodes and GPUs
curl -s localhost:8083/api/v1/placements     # placements
```

Other ports: webhook 8443, metrics 8080, health probes 8081.
