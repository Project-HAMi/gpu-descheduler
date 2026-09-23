package cache

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"k8s.io/klog/v2"
)

const (
	DefaultPersistenceDir = "/etc/crdplacement"
	PersistenceFileName   = "consumed_reqids.json"
)

var persistenceMu sync.Mutex
var persistenceDir string = DefaultPersistenceDir

func SetPersistenceDir(dir string) {
	persistenceMu.Lock()
	defer persistenceMu.Unlock()
	persistenceDir = dir
}

func GetPersistenceDir() string {
	persistenceMu.Lock()
	defer persistenceMu.Unlock()
	return persistenceDir
}

func ensurePersistenceDir() error {
	dir := GetPersistenceDir()
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create persistence directory %s: %w", dir, err)
	}
	return nil
}

func getPersistenceFilePath() string {
	return filepath.Join(GetPersistenceDir(), PersistenceFileName)
}

func SaveConsumedReqIDsToFile() error {
	persistenceMu.Lock()
	defer persistenceMu.Unlock()

	if err := ensurePersistenceDir(); err != nil {
		return err
	}

	consumedReqMu.RLock()
	data := make(map[string]ConsumedReqInfo, len(consumedReqIDs))
	for k, v := range consumedReqIDs {
		data[k] = v
	}
	consumedReqMu.RUnlock()

	jsonData, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal consumed ReqIDs: %w", err)
	}

	tmpFile := getPersistenceFilePath() + ".tmp"
	if err := os.WriteFile(tmpFile, jsonData, 0644); err != nil {
		return fmt.Errorf("failed to write temp file: %w", err)
	}

	if err := os.Rename(tmpFile, getPersistenceFilePath()); err != nil {
		_ = os.Remove(tmpFile)
		return fmt.Errorf("failed to rename temp file: %w", err)
	}

	klog.V(3).InfoS("Saved consumed ReqIDs to file",
		"file", getPersistenceFilePath(),
		"count", len(data))

	return nil
}

func LoadConsumedReqIDsFromFile() error {
	filePath := getPersistenceFilePath()

	data, err := os.ReadFile(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			klog.InfoS("No persisted state file found, starting fresh",
				"file", filePath)
			return nil
		}
		return fmt.Errorf("failed to read persistence file: %w", err)
	}

	var storedData map[string]ConsumedReqInfo
	if err := json.Unmarshal(data, &storedData); err != nil {
		return fmt.Errorf("failed to unmarshal consumed ReqIDs: %w", err)
	}

	consumedReqMu.Lock()
	count := 0
	for reqID, info := range storedData {
		if _, exists := consumedReqIDs[reqID]; !exists {
			consumedReqIDs[reqID] = info
			count++
		}
	}
	consumedReqMu.Unlock()

	klog.InfoS("Loaded consumed ReqIDs from file",
		"file", filePath,
		"loadedCount", count,
		"totalInMemory", len(storedData))

	return nil
}

func CleanUpOldPersistenceFile() error {
	filePath := getPersistenceFilePath()
	if _, err := os.Stat(filePath); err == nil {
		if err := os.Remove(filePath); err != nil {
			return fmt.Errorf("failed to remove old persistence file: %w", err)
		}
		klog.InfoS("Cleaned up old persistence file",
			"file", filePath)
	}
	return nil
}
