package state

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"crawlsync/internal/urlmanager"
)

func TestCheckpoint_SerializationAndValidation(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "crawlsync_checkpoint_test_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	cm := NewCheckpointManager(tempDir)

	cfg := CheckpointConfig{
		StartURL:       "https://example.com",
		WorkerCount:    5,
		MaxPages:       20,
		MaxDepth:       3,
		RequestTimeout: "10s",
		SameDomainOnly: true,
	}

	snap := urlmanager.Snapshot{
		Stats: urlmanager.Stats{
			TotalDiscovered: 2,
			Completed:       1,
			Pending:         1,
		},
		URLs: []urlmanager.URLItem{
			{
				Sequence:     1,
				URL:          "https://example.com",
				Normalized:   "https://example.com/",
				Status:       urlmanager.StatusCompleted,
				Depth:        0,
				DiscoveredAt: time.Now().UTC().Add(-10 * time.Second),
				UpdatedAt:    time.Now().UTC(),
				StatusCode:   200,
				Size:         1024,
				DurationMs:   120,
				SavedPath:    "data/pages/example.html",
			},
			{
				Sequence:     2,
				URL:          "https://example.com/about",
				Normalized:   "https://example.com/about",
				Status:       urlmanager.StatusPending,
				Depth:        1,
				DiscoveredAt: time.Now().UTC(),
				UpdatedAt:    time.Now().UTC(),
			},
		},
		LastSequence: 2,
		CapturedAt:   time.Now().UTC(),
	}

	sessionID, err := GenerateSessionID()
	if err != nil {
		t.Fatalf("GenerateSessionID failed: %v", err)
	}
	cp, err := cm.Save(sessionID, cfg, snap)
	if err != nil {
		t.Fatalf("Checkpoint save failed: %v", err)
	}

	if cp.Version != SchemaVersion {
		t.Errorf("Version = %d, want %d", cp.Version, SchemaVersion)
	}
	if cp.Sequence != 1 {
		t.Errorf("Sequence = %d, want 1", cp.Sequence)
	}

	// Verify on-disk file can be loaded and validated
	targetPath := filepath.Join(tempDir, CheckpointFileName)
	loaded, err := Load(targetPath)
	if err != nil {
		t.Fatalf("Failed to load saved checkpoint: %v", err)
	}

	if loaded.SessionID != sessionID {
		t.Errorf("Loaded SessionID = %s, want %s", loaded.SessionID, sessionID)
	}
	if loaded.Stats.TotalDiscovered != 2 || loaded.Stats.Completed != 1 {
		t.Errorf("Loaded stats mismatch: %+v", loaded.Stats)
	}
	if len(loaded.URLs) != 2 {
		t.Fatalf("Loaded URLs count = %d, want 2", len(loaded.URLs))
	}
	if loaded.URLs[0].Sequence != 1 || loaded.URLs[0].SavedPath != "data/pages/example.html" {
		t.Errorf("Loaded URL[0] mismatch: %+v", loaded.URLs[0])
	}
}

func TestCheckpoint_AtomicRotationAndBackup(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "crawlsync_rotation_test_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	cm := NewCheckpointManager(tempDir)
	sessionID := "test_session_rotation"
	cfg := CheckpointConfig{StartURL: "https://example.com"}

	// Save Checkpoint 1
	snap1 := urlmanager.Snapshot{
		Stats: urlmanager.Stats{TotalDiscovered: 1, Completed: 1},
		URLs: []urlmanager.URLItem{
			{Sequence: 1, URL: "https://example.com", Status: urlmanager.StatusCompleted},
		},
	}
	cp1, err := cm.Save(sessionID, cfg, snap1)
	if err != nil {
		t.Fatalf("Save 1 failed: %v", err)
	}
	if cp1.Sequence != 1 {
		t.Errorf("cp1 Sequence = %d, want 1", cp1.Sequence)
	}

	targetPath := filepath.Join(tempDir, CheckpointFileName)
	backupPath := filepath.Join(tempDir, BackupFileName)

	// After save 1: targetPath exists, backupPath does not exist yet
	if _, err := os.Stat(targetPath); err != nil {
		t.Fatalf("targetPath does not exist after first save: %v", err)
	}
	if _, err := os.Stat(backupPath); err == nil {
		t.Fatalf("backupPath should not exist after first save")
	}

	// Save Checkpoint 2
	snap2 := urlmanager.Snapshot{
		Stats: urlmanager.Stats{TotalDiscovered: 2, Completed: 2},
		URLs: []urlmanager.URLItem{
			{Sequence: 1, URL: "https://example.com", Status: urlmanager.StatusCompleted},
			{Sequence: 2, URL: "https://example.com/2", Status: urlmanager.StatusCompleted},
		},
	}
	cp2, err := cm.Save(sessionID, cfg, snap2)
	if err != nil {
		t.Fatalf("Save 2 failed: %v", err)
	}
	if cp2.Sequence != 2 {
		t.Errorf("cp2 Sequence = %d, want 2", cp2.Sequence)
	}

	// After save 2: both targetPath and backupPath must exist
	loadedLatest, err := Load(targetPath)
	if err != nil {
		t.Fatalf("failed to load latest checkpoint: %v", err)
	}
	if loadedLatest.Sequence != 2 || loadedLatest.Stats.TotalDiscovered != 2 {
		t.Errorf("latest checkpoint content mismatch: %+v", loadedLatest)
	}

	loadedBackup, err := Load(backupPath)
	if err != nil {
		t.Fatalf("failed to load backup checkpoint: %v", err)
	}
	if loadedBackup.Sequence != 1 || loadedBackup.Stats.TotalDiscovered != 1 {
		t.Errorf("backup checkpoint content mismatch: %+v", loadedBackup)
	}

	// Verify no temporary files (*.tmp) were left behind
	files, _ := os.ReadDir(tempDir)
	for _, f := range files {
		if filepath.Ext(f.Name()) == ".tmp" {
			t.Errorf("abandoned temporary file found: %s", f.Name())
		}
	}
}

func TestCheckpoint_ValidationErrors(t *testing.T) {
	// 1. Nil checkpoint
	if err := Validate(nil); err == nil {
		t.Error("expected error for nil checkpoint")
	}

	// 2. Unsupported version
	cpBadVersion := &Checkpoint{
		Version:   999,
		SessionID: "s1",
		CreatedAt: time.Now(),
		Config:    CheckpointConfig{StartURL: "https://example.com"},
	}
	if err := Validate(cpBadVersion); err == nil {
		t.Error("expected error for unsupported schema version")
	}

	// 3. Missing session ID
	cpNoSession := &Checkpoint{
		Version:   SchemaVersion,
		SessionID: "",
		CreatedAt: time.Now(),
		Config:    CheckpointConfig{StartURL: "https://example.com"},
	}
	if err := Validate(cpNoSession); err == nil {
		t.Error("expected error for missing session ID")
	}

	// 4. Missing StartURL
	cpNoURL := &Checkpoint{
		Version:   SchemaVersion,
		SessionID: "s1",
		CreatedAt: time.Now(),
		Config:    CheckpointConfig{StartURL: ""},
	}
	if err := Validate(cpNoURL); err == nil {
		t.Error("expected error for missing StartURL")
	}
}

func TestCheckpoint_ConcurrentSaves(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "crawlsync_concsave_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	cm := NewCheckpointManager(tempDir)
	sessionID := "test_concurrent"
	cfg := CheckpointConfig{StartURL: "https://example.com"}

	var wg sync.WaitGroup
	const numWriters = 20

	for i := 0; i < numWriters; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			snap := urlmanager.Snapshot{
				Stats: urlmanager.Stats{TotalDiscovered: id + 1},
			}
			_, err := cm.Save(sessionID, cfg, snap)
			if err != nil {
				t.Errorf("Concurrent save error: %v", err)
			}
		}(i)
	}

	wg.Wait()

	// Verify valid checkpoint exists at targetPath
	targetPath := filepath.Join(tempDir, CheckpointFileName)
	loaded, err := Load(targetPath)
	if err != nil {
		t.Fatalf("Failed loading checkpoint after concurrent saves: %v", err)
	}
	if loaded.Sequence != numWriters {
		t.Errorf("Final sequence = %d, want %d", loaded.Sequence, numWriters)
	}
}

func TestGenerateSessionID(t *testing.T) {
	seen := make(map[string]bool)
	const count = 50

	for i := 0; i < count; i++ {
		id, err := GenerateSessionID()
		if err != nil {
			t.Fatalf("GenerateSessionID failed on iteration %d: %v", i, err)
		}
		if len(id) < 15 {
			t.Errorf("GenerateSessionID returned unexpectedly short ID: %s", id)
		}
		if seen[id] {
			t.Errorf("Collision detected in GenerateSessionID: %s", id)
		}
		seen[id] = true
	}
}


