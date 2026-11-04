package state

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"crawlsync/internal/urlmanager"
)

func validTestCheckpoint() *Checkpoint {
	now := time.Now().UTC()
	return &Checkpoint{
		Version:           SchemaVersion,
		SessionID:         "session_test_12345",
		CreatedAt:         now,
		Sequence:          3,
		CumulativeElapsed: 5 * time.Second,
		Config: CheckpointConfig{
			StartURL:       "https://example.com/",
			WorkerCount:    4,
			MaxPages:       20,
			MaxDepth:       3,
			RequestTimeout: "10s",
			SameDomainOnly: true,
		},
		Stats: urlmanager.Stats{
			TotalDiscovered: 3,
			Pending:         1,
			InProgress:      0,
			Completed:       2,
			Failed:          0,
		},
		URLs: []urlmanager.URLItem{
			{
				Sequence:     1,
				URL:          "https://example.com/",
				Normalized:   "https://example.com/",
				Status:       urlmanager.StatusCompleted,
				Depth:        0,
				DiscoveredAt: now,
				UpdatedAt:    now,
				StatusCode:   200,
				SavedPath:    filepath.Join("data", "pages", "page_1.html"),
			},
			{
				Sequence:     2,
				URL:          "https://example.com/p1",
				Normalized:   "https://example.com/p1",
				Status:       urlmanager.StatusCompleted,
				Depth:        1,
				DiscoveredAt: now,
				UpdatedAt:    now,
				StatusCode:   200,
				SavedPath:    filepath.Join("data", "pages", "page_2.html"),
			},
			{
				Sequence:     3,
				URL:          "https://example.com/p2",
				Normalized:   "https://example.com/p2",
				Status:       urlmanager.StatusPending,
				Depth:        1,
				DiscoveredAt: now,
				UpdatedAt:    now,
			},
		},
	}
}

func TestRecovery_MissingPrimary_FailsWithoutFallback(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "crawlsync_rec_missing_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Even if checkpoint.prev.json exists, missing checkpoint.json must fail
	backupPath := filepath.Join(tempDir, BackupFileName)
	if err := os.WriteFile(backupPath, []byte(`{"version":1}`), 0644); err != nil {
		t.Fatalf("failed writing backup: %v", err)
	}

	opts := RecoveryValidationOptions{
		AllowLocal: false,
		StorageDir: filepath.Join(tempDir, "pages"),
	}

	_, err = LoadWithFallback(tempDir, opts)
	if err == nil {
		t.Fatal("expected error when primary checkpoint is missing, got nil")
	}
	if !strings.Contains(err.Error(), "no checkpoint found") {
		t.Errorf("expected 'no checkpoint found' error, got: %v", err)
	}
}

func TestRecovery_CorruptPrimary_FallbackToBackup(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "crawlsync_rec_corrupt_fallback_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	storageDir := filepath.Join(tempDir, "pages")
	_ = os.MkdirAll(storageDir, 0755)

	opts := RecoveryValidationOptions{
		AllowLocal: false,
		StorageDir: storageDir,
	}

	// 1. Write corrupt primary
	primaryPath := filepath.Join(tempDir, CheckpointFileName)
	if err := os.WriteFile(primaryPath, []byte("NOT_JSON_DATA_GARBAGE"), 0644); err != nil {
		t.Fatalf("failed writing primary: %v", err)
	}

	// 2. Write valid backup
	cm := NewCheckpointManager(tempDir)
	validCP := validTestCheckpoint()
	validCP.URLs[0].SavedPath = filepath.Join(storageDir, "page_1.html")
	validCP.URLs[1].SavedPath = filepath.Join(storageDir, "page_2.html")

	snap := urlmanager.Snapshot{
		Stats:        validCP.Stats,
		URLs:         validCP.URLs,
		LastSequence: 3,
		CapturedAt:   time.Now(),
	}

	backupPath := filepath.Join(tempDir, BackupFileName)
	if _, err := cm.Save(validCP.SessionID, validCP.Config, snap, validCP.CumulativeElapsed); err != nil {
		t.Fatalf("cm.Save failed: %v", err)
	}
	// Move checkpoint.json to checkpoint.prev.json, then write corrupt primary
	_ = os.Rename(primaryPath, backupPath)
	_ = os.WriteFile(primaryPath, []byte("{ corrupt json ..."), 0644)

	// 3. Attempt recovery with fallback
	recovered, err := LoadWithFallback(tempDir, opts)
	if err != nil {
		t.Fatalf("LoadWithFallback failed: %v", err)
	}

	if recovered.SessionID != validCP.SessionID {
		t.Errorf("recovered session = %s, want %s", recovered.SessionID, validCP.SessionID)
	}

	// Verify primary was renamed to checkpoint.corrupt.<timestamp>
	entries, _ := os.ReadDir(tempDir)
	foundCorrupt := false
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "checkpoint.corrupt.") {
			foundCorrupt = true
			break
		}
	}
	if !foundCorrupt {
		t.Error("expected corrupt primary to be renamed with checkpoint.corrupt.* prefix")
	}
}

func TestRecovery_BothCorrupt_FailsSafely(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "crawlsync_rec_both_corrupt_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	primaryPath := filepath.Join(tempDir, CheckpointFileName)
	backupPath := filepath.Join(tempDir, BackupFileName)

	_ = os.WriteFile(primaryPath, []byte("BAD_DATA_1"), 0644)
	_ = os.WriteFile(backupPath, []byte("BAD_DATA_2"), 0644)

	opts := RecoveryValidationOptions{
		AllowLocal: false,
		StorageDir: filepath.Join(tempDir, "pages"),
	}

	_, err = LoadWithFallback(tempDir, opts)
	if err == nil {
		t.Fatal("expected failure when both primary and backup are corrupt")
	}
	if !strings.Contains(err.Error(), "both primary and backup checkpoints are unrecoverable") {
		t.Errorf("expected 'both primary and backup checkpoints are unrecoverable' error, got: %v", err)
	}
}

func TestRecovery_Validation_OutOfDomainRejected(t *testing.T) {
	opts := RecoveryValidationOptions{
		AllowLocal: false,
		StorageDir: "data/pages",
	}

	cp := validTestCheckpoint()
	cp.Config.SameDomainOnly = true
	// Inject out-of-domain URL
	cp.URLs = append(cp.URLs, urlmanager.URLItem{
		Sequence:     4,
		URL:          "https://evil.com/phishing",
		Normalized:   "https://evil.com/phishing",
		Status:       urlmanager.StatusPending,
		Depth:        1,
		DiscoveredAt: time.Now(),
		UpdatedAt:    time.Now(),
	})

	err := ValidateForRecovery(cp, opts)
	if err == nil {
		t.Fatal("expected validation failure for out-of-domain URL")
	}
	if !strings.Contains(err.Error(), "same-domain restriction") {
		t.Errorf("expected same-domain violation, got: %v", err)
	}
}

func TestRecovery_Validation_NonHTTPRejected(t *testing.T) {
	opts := RecoveryValidationOptions{
		AllowLocal: false,
		StorageDir: "data/pages",
	}

	cp := validTestCheckpoint()
	cp.URLs = append(cp.URLs, urlmanager.URLItem{
		Sequence:     4,
		URL:          "ftp://example.com/file.txt",
		Normalized:   "ftp://example.com/file.txt",
		Status:       urlmanager.StatusPending,
		Depth:        1,
		DiscoveredAt: time.Now(),
		UpdatedAt:    time.Now(),
	})

	err := ValidateForRecovery(cp, opts)
	if err == nil {
		t.Fatal("expected validation failure for non-HTTP scheme")
	}
}

func TestRecovery_Validation_PrivateIPBlockedUnlessAllowed(t *testing.T) {
	cp := validTestCheckpoint()
	cp.Config.StartURL = "http://127.0.0.1:8080/"
	cp.URLs[0].URL = "http://127.0.0.1:8080/"
	cp.URLs[0].Normalized = "http://127.0.0.1:8080/"

	// Blocked without AllowLocal
	optsBlocked := RecoveryValidationOptions{
		AllowLocal: false,
		StorageDir: "data/pages",
	}
	if err := ValidateForRecovery(cp, optsBlocked); err == nil {
		t.Fatal("expected private IP to be blocked when AllowLocal is false")
	}

	// Allowed with AllowLocal
	optsAllowed := RecoveryValidationOptions{
		AllowLocal: true,
		StorageDir: "data/pages",
	}
	cp.URLs = []urlmanager.URLItem{cp.URLs[0]} // trim down
	cp.URLs[0].SavedPath = filepath.Join("data", "pages", "page_1.html")
	if err := ValidateForRecovery(cp, optsAllowed); err != nil {
		t.Fatalf("expected private IP to pass when AllowLocal is true, got: %v", err)
	}
}

func TestRecovery_Validation_PathTraversalRejected(t *testing.T) {
	opts := RecoveryValidationOptions{
		AllowLocal: false,
		StorageDir: filepath.Join("data", "pages"),
	}

	cp := validTestCheckpoint()
	// Malicious completed URL attempting path traversal
	cp.URLs[0].SavedPath = filepath.Join("data", "pages", "..", "..", "etc", "passwd")

	err := ValidateForRecovery(cp, opts)
	if err == nil {
		t.Fatal("expected path traversal in SavedPath to be rejected")
	}
	if !strings.Contains(err.Error(), "security violation") {
		t.Errorf("expected security violation error, got: %v", err)
	}
}

func TestRecovery_Validation_DuplicateSequenceRejected(t *testing.T) {
	opts := RecoveryValidationOptions{
		AllowLocal: false,
		StorageDir: "data/pages",
	}

	cp := validTestCheckpoint()
	// Duplicate sequence number 2
	cp.URLs[2].Sequence = 2

	err := ValidateForRecovery(cp, opts)
	if err == nil {
		t.Fatal("expected duplicate sequence number to be rejected")
	}
	if !strings.Contains(err.Error(), "duplicate sequence") {
		t.Errorf("expected duplicate sequence error, got: %v", err)
	}
}

func TestRecovery_Validation_NonPositiveSequenceRejected(t *testing.T) {
	opts := RecoveryValidationOptions{
		AllowLocal: false,
		StorageDir: "data/pages",
	}

	cp := validTestCheckpoint()
	cp.URLs[0].Sequence = 0

	err := ValidateForRecovery(cp, opts)
	if err == nil {
		t.Fatal("expected sequence 0 to be rejected")
	}
	if !strings.Contains(err.Error(), "invalid non-positive sequence") {
		t.Errorf("expected non-positive sequence error, got: %v", err)
	}
}

func TestRecovery_Validation_DepthExceededRejected(t *testing.T) {
	opts := RecoveryValidationOptions{
		AllowLocal: false,
		StorageDir: "data/pages",
	}

	cp := validTestCheckpoint()
	cp.Config.MaxDepth = 2
	cp.URLs[2].Depth = 5 // exceeds maxDepth

	err := ValidateForRecovery(cp, opts)
	if err == nil {
		t.Fatal("expected depth exceeding maxDepth to be rejected")
	}
	if !strings.Contains(err.Error(), "exceeds configured maxDepth") {
		t.Errorf("expected maxDepth exceeded error, got: %v", err)
	}
}

func createValidBackup(t *testing.T, dir, storageDir string) *Checkpoint {
	t.Helper()
	cm := NewCheckpointManager(dir)
	validCP := validTestCheckpoint()
	validCP.URLs[0].SavedPath = filepath.Join(storageDir, "page_1.html")
	validCP.URLs[1].SavedPath = filepath.Join(storageDir, "page_2.html")

	_ = os.WriteFile(validCP.URLs[0].SavedPath, []byte("<html>P1</html>"), 0644)
	_ = os.WriteFile(validCP.URLs[1].SavedPath, []byte("<html>P2</html>"), 0644)

	snap := urlmanager.Snapshot{
		Stats:        validCP.Stats,
		URLs:         validCP.URLs,
		LastSequence: 3,
		CapturedAt:   time.Now(),
	}

	backupPath := filepath.Join(dir, BackupFileName)
	primaryPath := filepath.Join(dir, CheckpointFileName)

	if _, err := cm.Save(validCP.SessionID, validCP.Config, snap, validCP.CumulativeElapsed); err != nil {
		t.Fatalf("cm.Save failed: %v", err)
	}
	_ = os.Rename(primaryPath, backupPath)
	return validCP
}

func TestRecovery_LoadWithFallback_EmptyPrimaryFallsBackToBackup(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "crawlsync_rec_empty_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	storageDir := filepath.Join(tempDir, "pages")
	_ = os.MkdirAll(storageDir, 0755)

	validCP := createValidBackup(t, tempDir, storageDir)

	// Write empty primary (0 bytes)
	primaryPath := filepath.Join(tempDir, CheckpointFileName)
	if err := os.WriteFile(primaryPath, []byte(""), 0644); err != nil {
		t.Fatalf("failed writing empty primary: %v", err)
	}

	opts := RecoveryValidationOptions{
		AllowLocal: false,
		StorageDir: storageDir,
	}

	recovered, err := LoadWithFallback(tempDir, opts)
	if err != nil {
		t.Fatalf("LoadWithFallback failed on empty primary: %v", err)
	}
	if recovered.SessionID != validCP.SessionID {
		t.Errorf("recovered session = %s, want %s", recovered.SessionID, validCP.SessionID)
	}
}

func TestRecovery_LoadWithFallback_TruncatedJSONFallsBackToBackup(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "crawlsync_rec_trunc_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	storageDir := filepath.Join(tempDir, "pages")
	_ = os.MkdirAll(storageDir, 0755)

	validCP := createValidBackup(t, tempDir, storageDir)

	// Write truncated JSON primary
	primaryPath := filepath.Join(tempDir, CheckpointFileName)
	truncated := []byte(`{"version":1,"sessionId":"sess_test","sequence":2,"urls":[{"sequence":1`)
	if err := os.WriteFile(primaryPath, truncated, 0644); err != nil {
		t.Fatalf("failed writing truncated primary: %v", err)
	}

	opts := RecoveryValidationOptions{
		AllowLocal: false,
		StorageDir: storageDir,
	}

	recovered, err := LoadWithFallback(tempDir, opts)
	if err != nil {
		t.Fatalf("LoadWithFallback failed on truncated primary: %v", err)
	}
	if recovered.SessionID != validCP.SessionID {
		t.Errorf("recovered session = %s, want %s", recovered.SessionID, validCP.SessionID)
	}
}

func TestRecovery_Validation_UnsupportedVersionRejected(t *testing.T) {
	opts := RecoveryValidationOptions{
		AllowLocal: false,
		StorageDir: "data/pages",
	}

	cp := validTestCheckpoint()
	cp.Version = 99 // unsupported schema version

	err := ValidateForRecovery(cp, opts)
	if err == nil {
		t.Fatal("expected unsupported version to be rejected")
	}
	if !strings.Contains(err.Error(), "unsupported checkpoint schema version") {
		t.Errorf("expected version mismatch error, got: %v", err)
	}
}

func TestRecovery_Validation_SymlinkEscapeRejected(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "crawlsync_rec_symlink_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	storageDir := filepath.Join(tempDir, "pages")
	_ = os.MkdirAll(storageDir, 0755)

	// Create an outside secret file
	secretFile := filepath.Join(tempDir, "secret.txt")
	if err := os.WriteFile(secretFile, []byte("TOP_SECRET_PASSWORD"), 0644); err != nil {
		t.Fatalf("failed creating secret file: %v", err)
	}

	// Attempt to create symlink inside storageDir pointing to secretFile
	symlinkPath := filepath.Join(storageDir, "page_1.html")
	if err := os.Symlink(secretFile, symlinkPath); err != nil {
		t.Logf("os.Symlink not permitted without elevation (%v); tested in environments where supported", err)
	} else {
		cp := validTestCheckpoint()
		cp.URLs[0].SavedPath = symlinkPath

		opts := RecoveryValidationOptions{
			AllowLocal: false,
			StorageDir: storageDir,
		}

		err := ValidateForRecovery(cp, opts)
		if err == nil {
			t.Fatal("expected symlink escaping storage directory to be rejected by ValidateForRecovery")
		}
		if !strings.Contains(err.Error(), "security violation") {
			t.Errorf("expected security violation for symlink escape, got: %v", err)
		}

		// Also verify urlmanager.Restore demotes it
		mgr := urlmanager.NewManager()
		_ = mgr.Restore([]urlmanager.URLItem{cp.URLs[0]}, urlmanager.RestoreOptions{
			StorageDir: storageDir,
		})
		items := mgr.GetAllItems()
		if len(items) != 1 || items[0].Status != urlmanager.StatusPending {
			t.Errorf("expected symlink escape to be demoted to pending by Restore, got %+v", items)
		}
	}
}

func TestRecovery_Validation_WindowsPathTraversalRejected(t *testing.T) {
	opts := RecoveryValidationOptions{
		AllowLocal: false,
		StorageDir: filepath.Join("data", "pages"),
	}

	cp := validTestCheckpoint()
	// Malicious completed URL attempting Windows-style path traversal with backslashes
	cp.URLs[0].SavedPath = `data\pages\..\..\windows\system.ini`

	err := ValidateForRecovery(cp, opts)
	if err == nil {
		t.Fatal("expected Windows backslash path traversal to be rejected")
	}
	if !strings.Contains(err.Error(), "security violation") {
		t.Errorf("expected security violation error, got: %v", err)
	}
}
