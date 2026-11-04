package state

import (
	"errors"
	"fmt"
	"log"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"crawlsync/internal/parser"
	"crawlsync/internal/urlmanager"
)

// RecoveryValidationOptions defines constraints used to validate a recovered checkpoint.
type RecoveryValidationOptions struct {
	AllowLocal bool
	StorageDir string
}

// LoadWithFallback loads checkpoint.json from dir, validating its schema, integrity,
// and safety constraints. If checkpoint.json does not exist, it returns a clear "no checkpoint found" error.
// If checkpoint.json is corrupt, it renames it to checkpoint.corrupt.<timestamp>, logs a warning,
// and falls back to checkpoint.prev.json.
//
// OS CONCEPT - FAULT TOLERANCE & CRASH RECOVERY:
// Systems subject to power failures or abrupt termination may suffer partial file corruption.
// A multi-stage recovery policy (primary verification -> corruption quarantine -> backup fallback)
// ensures the process can recover the latest consistent state without manual file repair.
func LoadWithFallback(dir string, opts RecoveryValidationOptions) (*Checkpoint, error) {
	primaryPath := filepath.Join(dir, CheckpointFileName)
	backupPath := filepath.Join(dir, BackupFileName)

	// 1. Missing primary checkpoint check
	if _, err := os.Stat(primaryPath); os.IsNotExist(err) {
		return nil, fmt.Errorf("no checkpoint found in %s: cannot resume", dir)
	}

	// 2. Attempt loading primary checkpoint
	primaryCP, err := Load(primaryPath)
	if err == nil {
		if valErr := ValidateForRecovery(primaryCP, opts); valErr == nil {
			return primaryCP, nil
		} else {
			err = valErr
		}
	}

	// 3. Primary is corrupt: Quarantine it to checkpoint.corrupt.<timestamp>
	corruptName := fmt.Sprintf("checkpoint.corrupt.%d", time.Now().UnixNano())
	corruptPath := filepath.Join(dir, corruptName)
	if renameErr := os.Rename(primaryPath, corruptPath); renameErr != nil {
		log.Printf("[Recovery WARN] Failed renaming corrupt checkpoint %s: %v", primaryPath, renameErr)
	}

	log.Printf("[Recovery WARN] Primary checkpoint is corrupt (%v); quarantined to %s. Attempting fallback to %s...",
		err, corruptName, BackupFileName)

	// 4. Attempt fallback to backup checkpoint
	if _, statErr := os.Stat(backupPath); os.IsNotExist(statErr) {
		return nil, fmt.Errorf("primary checkpoint is corrupt (%v) and backup %s does not exist", err, BackupFileName)
	}

	backupCP, backupErr := Load(backupPath)
	if backupErr != nil {
		return nil, fmt.Errorf("both primary and backup checkpoints are unrecoverable (backup load error: %w)", backupErr)
	}

	if valErr := ValidateForRecovery(backupCP, opts); valErr != nil {
		return nil, fmt.Errorf("both primary and backup checkpoints are unrecoverable (backup validation error: %w)", valErr)
	}

	log.Printf("[Recovery INFO] Successfully recovered session %s from backup checkpoint (sequence #%d)",
		backupCP.SessionID, backupCP.Sequence)
	return backupCP, nil
}

// ValidateForRecovery rigorously validates checkpoint contents as untrusted input.
//
// Validates:
// 1. Schema version and mandatory metadata.
// 2. Configuration parameters (worker range, max pages, max depth).
// 3. URLs: canonical normalization, scheme (http/https only), allowed domain, and unique positive sequence numbers.
// 4. SSRF safety: blocks loopback and RFC 1918 private IPs unless AllowLocal is explicitly enabled.
// 5. Filesystem safety: verifies completed HTML file paths reside strictly within the storage directory.
func ValidateForRecovery(cp *Checkpoint, opts RecoveryValidationOptions) error {
	if cp == nil {
		return errors.New("checkpoint is nil")
	}

	// 1. Basic schema validation
	if err := Validate(cp); err != nil {
		return fmt.Errorf("schema validation failed: %w", err)
	}

	// 2. Sane configuration bounds
	if cp.Config.WorkerCount < 1 || cp.Config.WorkerCount > 20 {
		return fmt.Errorf("invalid workerCount %d in checkpoint (must be 1-20)", cp.Config.WorkerCount)
	}
	if cp.Config.MaxPages <= 0 {
		return fmt.Errorf("invalid maxPages %d in checkpoint (must be positive)", cp.Config.MaxPages)
	}
	if cp.Config.MaxDepth < 0 {
		return fmt.Errorf("invalid maxDepth %d in checkpoint (must be non-negative)", cp.Config.MaxDepth)
	}
	if cp.CumulativeElapsed < 0 {
		return fmt.Errorf("invalid cumulativeElapsed %v in checkpoint", cp.CumulativeElapsed)
	}

	// 3. Seed URL parsing & validation
	startURL, err := url.Parse(cp.Config.StartURL)
	if err != nil {
		return fmt.Errorf("invalid startURL %q in checkpoint: %w", cp.Config.StartURL, err)
	}
	startScheme := strings.ToLower(startURL.Scheme)
	if startScheme != "http" && startScheme != "https" {
		return fmt.Errorf("unsupported startURL scheme %q (only http/https allowed)", startURL.Scheme)
	}
	if !opts.AllowLocal && isDisallowedHostname(startURL.Hostname()) {
		return fmt.Errorf("startURL %s references local or private IP, blocked without --allow-local", cp.Config.StartURL)
	}

	// 4. Validate URL items
	seenSeq := make(map[int64]bool)
	seenNorm := make(map[string]bool)

	for idx, item := range cp.URLs {
		// Sequence number validation: strictly positive and unique
		if item.Sequence <= 0 {
			return fmt.Errorf("URL item #%d has invalid non-positive sequence %d", idx, item.Sequence)
		}
		if seenSeq[item.Sequence] {
			return fmt.Errorf("duplicate sequence number %d detected in checkpoint", item.Sequence)
		}
		seenSeq[item.Sequence] = true

		// Status validation
		switch item.Status {
		case urlmanager.StatusPending, urlmanager.StatusInProgress, urlmanager.StatusCompleted, urlmanager.StatusFailed:
			// Valid
		default:
			return fmt.Errorf("URL %s has unknown status %q", item.URL, item.Status)
		}

		// Depth validation
		if item.Depth < 0 {
			return fmt.Errorf("URL %s has negative depth %d", item.URL, item.Depth)
		}
		if cp.Config.MaxDepth > 0 && item.Depth > cp.Config.MaxDepth {
			return fmt.Errorf("URL %s depth %d exceeds configured maxDepth %d", item.URL, item.Depth, cp.Config.MaxDepth)
		}

		// Canonical normalization check
		norm, err := urlmanager.NormalizeURL(item.URL)
		if err != nil {
			return fmt.Errorf("malformed URL %q at sequence %d: %w", item.URL, item.Sequence, err)
		}
		if norm != item.Normalized {
			return fmt.Errorf("normalized URL mismatch for %q: expected %q, got %q", item.URL, norm, item.Normalized)
		}

		if seenNorm[norm] {
			return fmt.Errorf("duplicate normalized URL %q detected in checkpoint", norm)
		}
		seenNorm[norm] = true

		// Scheme check
		itemParsed, err := url.Parse(norm)
		if err != nil {
			return fmt.Errorf("cannot parse normalized URL %q: %w", norm, err)
		}
		itemScheme := strings.ToLower(itemParsed.Scheme)
		if itemScheme != "http" && itemScheme != "https" {
			return fmt.Errorf("unsupported scheme %q in URL %q", itemParsed.Scheme, norm)
		}

		// Same-domain restriction check
		if cp.Config.SameDomainOnly {
			if !parser.IsSameDomain(startURL.Hostname(), itemParsed.Hostname()) {
				return fmt.Errorf("URL %q violates same-domain restriction against seed %q", norm, startURL.Hostname())
			}
		}

		// SSRF & Local IP check
		if !opts.AllowLocal && isDisallowedHostname(itemParsed.Hostname()) {
			return fmt.Errorf("URL %q references local or private network, blocked without --allow-local", norm)
		}

		// Storage path traversal protection
		targetStorageDir := opts.StorageDir
		if targetStorageDir == "" {
			targetStorageDir = cp.Config.StorageDir
		}
		if item.Status == urlmanager.StatusCompleted && item.SavedPath != "" && targetStorageDir != "" {
			// Normalize separators so Windows backslashes (\) are normalized on all platforms
			normalizedSaved := filepath.FromSlash(strings.ReplaceAll(item.SavedPath, "\\", "/"))
			absStorage, err1 := filepath.Abs(targetStorageDir)
			absSaved, err2 := filepath.Abs(normalizedSaved)
			if err1 != nil || err2 != nil {
				return fmt.Errorf("failed resolving storage paths for URL %q", norm)
			}

			rel, err3 := filepath.Rel(absStorage, absSaved)
			if err3 != nil || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
				return fmt.Errorf("security violation: SavedPath %q points outside storage directory %q", item.SavedPath, targetStorageDir)
			}
			if filepath.Dir(absSaved) != absStorage {
				return fmt.Errorf("security violation: SavedPath %q is not in storage directory %q", item.SavedPath, targetStorageDir)
			}

			// Symlink escape protection: ensure symlink targets do not escape storage directory
			realStorage, errS := filepath.EvalSymlinks(absStorage)
			if errS != nil {
				realStorage = absStorage
			}
			if realDir, errD := filepath.EvalSymlinks(filepath.Dir(absSaved)); errD == nil {
				relDir, errRel := filepath.Rel(realStorage, realDir)
				if errRel != nil || strings.HasPrefix(relDir, "..") || filepath.IsAbs(relDir) {
					return fmt.Errorf("security violation: SavedPath directory %q resolves outside storage directory (points to %q)", filepath.Dir(absSaved), realDir)
				}
			}
			if realTarget, errSym := filepath.EvalSymlinks(absSaved); errSym == nil {
				relTarget, errRel := filepath.Rel(realStorage, realTarget)
				if errRel != nil || strings.HasPrefix(relTarget, "..") || filepath.IsAbs(relTarget) {
					return fmt.Errorf("security violation: SavedPath symlink %q resolves outside storage directory (target %q)", item.SavedPath, realTarget)
				}
			}
		}
	}

	return nil
}

func isDisallowedHostname(host string) bool {
	h := strings.ToLower(host)
	if h == "localhost" || strings.HasSuffix(h, ".local") {
		return true
	}
	if strings.Contains(h, ":") {
		var err error
		h, _, err = net.SplitHostPort(h)
		if err != nil {
			return true
		}
	}

	ip := net.ParseIP(h)
	if ip != nil {
		return isDisallowedIP(ip)
	}

	// Resolve hostname to inspect underlying IPs
	ips, err := net.LookupIP(h)
	if err != nil {
		return false
	}
	for _, resolvedIP := range ips {
		if isDisallowedIP(resolvedIP) {
			return true
		}
	}
	return false
}

func isDisallowedIP(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified()
}

