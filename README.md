# HAMi GPU Descheduler

[![CI](https://img.shields.io/github/actions/workflow/status/Project-HAMi/gpu-descheduler/ci.yml?branch=main&label=CI)](https://github.com/Project-HAMi/gpu-descheduler/actions/workflows/ci.yml)
[![OpenSSF Scorecard](https://img.shields.io/ossf-scorecard/github.com/Project-HAMi/gpu-descheduler?label=OpenSSF%20Scorecard)](https://scorecard.dev/viewer/?uri=github.com/Project-HAMi/gpu-descheduler)
[![License](https://img.shields.io/github/license/Project-HAMi/gpu-descheduler)](LICENSE)

English | [中文](README_CN.md)

The descheduler moves GPU Pods on [HAMi](https://github.com/Project-HAMi/HAMi) clusters. You tell it where a Pod should go with a `HamiPodPlacement`. It can evict the old Pod, and it pins the next matching Pod to the GPUs you chose.

## Requirements

| Component | Version |
|-----------|---------|
| HAMi | v2.10.0 or later. Tested with v2.10.0 and v2.11.0. |
| Kubernetes | v1.24 or later. v0.0.3 is tested with v1.33, earlier releases with v1.24 and v1.36. |
| Helm | v3.8 or later |

## Install

Install a release:

```bash
helm install gpu-descheduler oci://docker.io/projecthami/gpu-descheduler-webhook \
  --version 0.0.3-chart --namespace kube-system
```

The chart lives next to the image on Docker Hub, tagged `<version>-chart`. Since v0.0.3, images are signed with cosign and ship an SBOM, and each GitHub release carries the chart package and its cosign bundle. To verify them:

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

Everything goes into the namespace you pass with `--namespace`. The chart runs one replica and refuses more, because the webhook matches placements from the leader's memory.

Check that it runs:

```bash
kubectl get pods -n kube-system -l app=gpu-descheduler-webhook
```

To remove it, including the CRD and the TLS Secret:

```bash
NAMESPACE=kube-system ./chart/uninstall-gpu-descheduler.sh
```

0.0.2 kept its state in `/etc/crdplacement` on the node it ran on. Later versions keep it in the placement status, so you can delete that directory after upgrading.

## Build

Build the image with `./build-image.sh`. Run `./build-image.sh -h` for options. Building the binary needs the Go version from `go.mod`.

The image holds only the static binary, on distroless, and runs as uid 65532. It has no shell.

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

A placement is used by one Pod. That Pod must use `schedulerName: hami-scheduler` and match `podSelector`. When several placements match a Pod, the oldest one is used. The placement status records the use, so a restart does not offer the placement again.

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

If the eviction is refused, for example by a PodDisruptionBudget, the placement fails and the eviction is not retried.

The freed capacity is not reserved. Another Pod can take it before the replacement is scheduled. See [#10](https://github.com/Project-HAMi/gpu-descheduler/issues/10). A reservation in the HAMi scheduler is planned in [Project-HAMi/HAMi#3541](https://github.com/Project-HAMi/HAMi/issues/3541).

### Phases

| Phase | Meaning |
|-------|---------|
| `EvictingOldPod` | Evicting the Pod in `evictPod` |
| `Pending` | Target checked, waiting for a matching Pod |
| `Matched` | A matching Pod was created with this placement |
| `Completed` | The matching Pod is running |
| `Failed` | Something went wrong. See `message`. |

## Debugging

The descheduler serves its state on port 8083:

```bash
kubectl -n kube-system port-forward deploy/gpu-descheduler-webhook 8083 &
curl -s localhost:8083/api/v1/cluster-info   # nodes and GPUs
curl -s localhost:8083/api/v1/placements     # placements
```

Other ports: webhook 8443, metrics 8080, health probes 8081.
