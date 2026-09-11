package main

import (
	"encoding/json"
	"net/http"
	"time"

	placementv1 "gpu-descheduler/pkg/gpu-descheduler/api/v1"
	"gpu-descheduler/pkg/gpu-descheduler/cache"
	clusterstate "gpu-descheduler/pkg/gpu-descheduler/cluster_state"

	"k8s.io/klog/v2"
)

type ClusterInfoResponse struct {
	Timestamp  time.Time          `json:"timestamp"`
	NodeCount  int                `json:"nodeCount"`
	TotalGPUs  int                `json:"totalGPUs"`
	Healthy    bool               `json:"healthy"`
	LastSyncAt time.Time          `json:"lastSyncAt"`
	SyncError  string             `json:"syncError,omitempty"`
	Nodes      []NodeInfoResponse `json:"nodes"`
}

type NodeInfoResponse struct {
	NodeName string            `json:"nodeName"`
	Healthy  bool              `json:"healthy"`
	GPUCount int               `json:"gpuCount"`
	Labels   map[string]string `json:"labels,omitempty"`
	Devices  []DeviceResponse  `json:"devices"`
}

type DeviceResponse struct {
	ID       string `json:"id"`
	Index    uint   `json:"index"`
	Type     string `json:"type"`
	Vendor   string `json:"vendor"`
	MemoryMB int32  `json:"memoryMB"`
	Cores    int32  `json:"cores"`
	Numa     int    `json:"numa"`
	Mode     string `json:"mode,omitempty"`
	Health   bool   `json:"health"`
}

func RegisterClusterInfoHandler(mux *http.ServeMux, clusterState *clusterstate.ClusterState) {
	mux.HandleFunc("/api/v1/cluster-info", func(w http.ResponseWriter, r *http.Request) {
		handleClusterInfo(w, r, clusterState)
	})

	klog.Info("Registered HTTP endpoint: GET /api/v1/cluster-info")
}

func handleClusterInfo(w http.ResponseWriter, r *http.Request, cs *clusterstate.ClusterState) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	nodes := cs.GetAllNodes()
	resp := buildClusterInfoResponse(cs, nodes)

	if err := json.NewEncoder(w).Encode(resp); err != nil {
		klog.ErrorS(err, "Failed to encode cluster info response")
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
}

func buildClusterInfoResponse(cs *clusterstate.ClusterState, nodes map[string]*clusterstate.NodeInfo) ClusterInfoResponse {
	resp := ClusterInfoResponse{
		Timestamp:  time.Now(),
		NodeCount:  len(nodes),
		Healthy:    true,
		LastSyncAt: cs.GetLastSyncTime(),
		Nodes:      make([]NodeInfoResponse, 0, len(nodes)),
	}

	syncErr := cs.GetSyncError()
	if syncErr != nil {
		resp.SyncError = syncErr.Error()
		resp.Healthy = false
	}

	for nodeName, nodeInfo := range nodes {
		nodeResp := NodeInfoResponse{
			NodeName: nodeName,
			Healthy:  nodeInfo.Healthy,
			GPUCount: len(nodeInfo.Devices),
			Labels:   nodeInfo.Labels,
			Devices:  make([]DeviceResponse, 0, len(nodeInfo.Devices)),
		}

		if !nodeInfo.Healthy {
			resp.Healthy = false
		}

		for _, device := range nodeInfo.Devices {
			resp.TotalGPUs++

			nodeResp.Devices = append(nodeResp.Devices, DeviceResponse{
				ID:       device.ID,
				Index:    device.Index,
				Type:     device.Type,
				Vendor:   device.DeviceVendor,
				MemoryMB: device.Devmem,
				Cores:    device.Devcore,
				Numa:     device.Numa,
				Mode:     device.Mode,
				Health:   device.Health,
			})
		}

		resp.Nodes = append(resp.Nodes, nodeResp)
	}

	return resp
}

type PlacementCacheResponse struct {
	Timestamp  time.Time               `json:"timestamp"`
	TotalCount int                     `json:"totalCount"`
	Placements []PlacementInfoResponse `json:"placements"`
}

type PlacementInfoResponse struct {
	Namespace      string               `json:"namespace"`
	Name           string               `json:"name"`
	ReqID          string               `json:"reqID"`
	NodeName       string               `json:"nodeName"`
	GPUDevices     []string             `json:"gpuDevices"`
	Phase          string               `json:"phase,omitempty"`
	Message        string               `json:"message,omitempty"`
	CreatedAt      string               `json:"createdAt"`
	ConsumedAt     string               `json:"consumedAt,omitempty"`
	MatchedPodName string               `json:"matchedPodName,omitempty"`
	Assignments    []AssignmentResponse `json:"assignments,omitempty"`
}

type AssignmentResponse struct {
	PodName   string `json:"podName"`
	GPUDevice string `json:"gpuDevice"`
}

func RegisterAllAPIHandlers(mux *http.ServeMux, clusterState *clusterstate.ClusterState, placementCache *cache.PlacementCache) {
	RegisterClusterInfoHandler(mux, clusterState)
	RegisterPlacementCacheHandler(mux, placementCache)
}

func RegisterPlacementCacheHandler(mux *http.ServeMux, placementCache *cache.PlacementCache) {
	mux.HandleFunc("/api/v1/placements", func(w http.ResponseWriter, r *http.Request) {
		handlePlacementCache(w, r, placementCache)
	})

	klog.Info("Registered HTTP endpoint: GET /api/v1/placements")
}

func handlePlacementCache(w http.ResponseWriter, r *http.Request, pc *cache.PlacementCache) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	placements := pc.ListAll()
	resp := buildPlacementCacheResponse(placements)

	if err := json.NewEncoder(w).Encode(resp); err != nil {
		klog.ErrorS(err, "Failed to encode placement cache response")
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
}

func buildPlacementCacheResponse(placements []*placementv1.HamiPodPlacement) PlacementCacheResponse {
	resp := PlacementCacheResponse{
		Timestamp:  time.Now(),
		TotalCount: len(placements),
		Placements: make([]PlacementInfoResponse, 0, len(placements)),
	}

	for _, pl := range placements {
		plInfo := PlacementInfoResponse{
			Namespace:      pl.Namespace,
			Name:           pl.Name,
			ReqID:          pl.Spec.ReqID,
			NodeName:       pl.Spec.NodeName,
			GPUDevices:     pl.Spec.GPUDevices,
			Phase:          pl.Status.Phase,
			Message:        pl.Status.Message,
			CreatedAt:      pl.CreationTimestamp.Format(time.RFC3339),
			MatchedPodName: pl.Status.MatchedPodName,
		}

		if pl.Status.ConsumedAt != nil {
			plInfo.ConsumedAt = pl.Status.ConsumedAt.Format(time.RFC3339)
		}

		for _, assignment := range pl.Status.AssignmentDetails {
			plInfo.Assignments = append(plInfo.Assignments, AssignmentResponse{
				PodName:   assignment.PodName,
				GPUDevice: assignment.GPUDevice,
			})
		}

		resp.Placements = append(resp.Placements, plInfo)
	}

	return resp
}
