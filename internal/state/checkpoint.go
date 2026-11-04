package state

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"crawlsync/internal/urlmanager"
)

// SchemaVersion defines the current checkpoint schema version.
// Phase 4 establishes version 1.
const SchemaVersion = 1

const (
	CheckpointFileName = "checkpoint.json"
	BackupFileName     = "checkpoint.prev.json"
)

// CheckpointConfig stores crawler parameters needed for documentation and recovery.
type CheckpointConfig struct {
	StartURL       string `json:"startURL"`
	WorkerCount    int    `json:"workerCount"`
	MaxPages       int    `json:"maxPages"`
	MaxDepth       int    `json:"maxDepth"`
	RequestTimeout string `json:"requestTimeout"`
	SameDomainOnly bool   `json:"sameDomainOnly"`
}

// Checkpoint represents the complete serialized state of a crawl session.
type Checkpoint struct {
	Version   int                  `json:"version"`
	SessionID string               `json:"sessionId"`
	CreatedAt time.Time            `json:"createdAt"`
	Sequence  int64                `json:"sequence"`
	Config    CheckpointConfig     `json:"config"`
	Stats     urlmanager.Stats     `json:"stats"`
	URLs      []urlmanager.URLItem `json:"urls"`
}

// CheckpointManager handles serialization, atomic file writing, backup rotation,
// and validation of checkpoint files.
//
// OS CONCEPT - ATOMIC FILE OPERATIONS & CRASH CONSISTENCY:
// Overwriting a live state file directly risks corruption if the process crashes mid-write.
// CheckpointManager writes to a temporary file, invokes fsync (f.Sync()) to flush OS write
// buffers to physical storage, and performs an atomic rename to replace the checkpoint.
type CheckpointManager struct {
	dir string
	mu  sync.Mutex // Ensures checkpoint writes are strictly serialized

	saveCount int64 // Monotonically increasing checkpoint save sequence
}

// NewCheckpointManager creates a new manager operating in the specified directory.
func NewCheckpointManager(dir string) *CheckpointManager {
	if dir == "" {
		dir = filepath.Join("data", "checkpoints")
	}
	return &CheckpointManager{
		dir: dir,
	}
}

// GenerateSessionID creates a unique session identifier combining a timestamp and cryptographically secure random bytes.
// Returns an error if the operating system's entropy source fails.
func GenerateSessionID() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("crypto/rand entropy failure: %w", err)
	}
	return fmt.Sprintf("session_%d_%s", time.Now().Unix(), hex.EncodeToString(b)), nil
}

// Save serializes a URLManager snapshot and atomically writes it to disk.
// The previous valid checkpoint is preserved as checkpoint.prev.json.
func (cm *CheckpointManager) Save(sessionID string, cfg CheckpointConfig, snap urlmanager.Snapshot) (*Checkpoint, error) {
	// SERIALIZE WRITES: Only one checkpoint operation executes at any time
	cm.mu.Lock()
	defer cm.mu.Unlock()

	cm.saveCount++

	cp := &Checkpoint{
		Version:   SchemaVersion,
		SessionID: sessionID,
		CreatedAt: time.Now().UTC(),
		Sequence:  cm.saveCount,
		Config:    cfg,
		Stats:     snap.Stats,
		URLs:      snap.URLs,
	}

	data, err := json.MarshalIndent(cp, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("json serialization failed: %w", err)
	}

	if err := os.MkdirAll(cm.dir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create checkpoint directory: %w", err)
	}

	targetPath := filepath.Join(cm.dir, CheckpointFileName)
	backupPath := filepath.Join(cm.dir, BackupFileName)

	// Create temporary file in the same directory (required for atomic filesystem rename across drives)
	tmpPattern := fmt.Sprintf("checkpoint-%d-*.tmp", time.Now().UnixNano())
	tmpFile, err := os.CreateTemp(cm.dir, tmpPattern)
	if err != nil {
		return nil, fmt.Errorf("failed to create temporary checkpoint file: %w", err)
	}
	tmpPath := tmpFile.Name()

	// Guarantee temporary file cleanup if any step fails before commit
	committed := false
	defer func() {
		if !committed {
			_ = os.Remove(tmpPath)
		}
	}()

	// Write complete JSON payload
	if _, err := tmpFile.Write(data); err != nil {
		_ = tmpFile.Close()
		return nil, fmt.Errorf("failed writing checkpoint data: %w", err)
	}

	// OS CONCEPT - FILESYSTEM FLUSH / DURABILITY:
	// Flush OS disk cache to storage media before closing to prevent zero-length files on power loss.
	if err := tmpFile.Sync(); err != nil {
		_ = tmpFile.Close()
		return nil, fmt.Errorf("failed to sync checkpoint file: %w", err)
	}

	if err := tmpFile.Close(); err != nil {
		return nil, fmt.Errorf("failed to close temporary checkpoint file: %w", err)
	}

	// Commit atomically with Windows compatibility and backup preservation
	if err := cm.commitFile(tmpPath, targetPath, backupPath); err != nil {
		return nil, fmt.Errorf("atomic checkpoint replacement failed: %w", err)
	}

	committed = true
	return cp, nil
}

// commitFile handles atomic replacement across Windows and POSIX while maintaining a backup.
func (cm *CheckpointManager) commitFile(tmpPath, targetPath, backupPath string) error {
	targetExists := false
	if _, err := os.Stat(targetPath); err == nil {
		targetExists = true
	}

	if targetExists {
		// Rotate existing checkpoint to backup
		_ = os.Remove(backupPath)
		if err := os.Rename(targetPath, backupPath); err != nil {
			return fmt.Errorf("failed to rotate existing checkpoint to backup: %w", err)
		}
	}

	// Move new temporary checkpoint into place
	if err := os.Rename(tmpPath, targetPath); err != nil {
		// Attempt rollback if rename failed and backup exists
		if targetExists {
			_ = os.Rename(backupPath, targetPath)
		}
		return fmt.Errorf("failed to rename tmp file to checkpoint: %w", err)
	}

	return nil
}

// Load reads and parses a checkpoint file from the given path.
func Load(path string) (*Checkpoint, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read checkpoint file: %w", err)
	}

	var cp Checkpoint
	if err := json.Unmarshal(data, &cp); err != nil {
		return nil, fmt.Errorf("checkpoint JSON unmarshal error: %w", err)
	}

	if err := Validate(&cp); err != nil {
		return nil, fmt.Errorf("checkpoint validation error: %w", err)
	}

	return &cp, nil
}

// Validate ensures a checkpoint matches the expected schema version and minimum structure.
func Validate(cp *Checkpoint) error {
	if cp == nil {
		return errors.New("checkpoint is nil")
	}
	if cp.Version != SchemaVersion {
		return fmt.Errorf("unsupported checkpoint schema version: %d (expected %d)", cp.Version, SchemaVersion)
	}
	if cp.SessionID == "" {
		return errors.New("missing session ID in checkpoint")
	}
	if cp.CreatedAt.IsZero() {
		return errors.New("missing creation timestamp in checkpoint")
	}
	if cp.Config.StartURL == "" {
		return errors.New("missing startURL in checkpoint configuration")
	}
	return nil
}

// SaveCount returns the total number of checkpoints saved by this manager.
func (cm *CheckpointManager) SaveCount() int64 {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	return cm.saveCount
}

// GetDirectory returns the configured checkpoint directory.
func (cm *CheckpointManager) GetDirectory() string {
	return cm.dir
}

