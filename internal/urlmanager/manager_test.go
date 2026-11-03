package urlmanager

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestURLManager_BasicTransitions(t *testing.T) {
	mgr := NewManager()

	sched, norm, err := mgr.TrySchedule("https://example.com/page1", 0)
	if err != nil || !sched {
		t.Fatalf("expected successful schedule, got sched=%t err=%v", sched, err)
	}

	item, exists := mgr.GetStatus(norm)
	if !exists || item.Status != StatusPending {
		t.Fatalf("expected StatusPending, got exists=%t item=%+v", exists, item)
	}
	if item.Sequence != 1 {
		t.Fatalf("expected sequence 1 for first item, got %d", item.Sequence)
	}

	// Try duplicate
	schedDup, _, err := mgr.TrySchedule("https://example.com/page1#section", 0)
	if err != nil || schedDup {
		t.Fatalf("expected duplicate to not be scheduled, got schedDup=%t err=%v", schedDup, err)
	}

	// Second unique URL should get sequence 2
	sched2, norm2, err := mgr.TrySchedule("https://example.com/page2", 1)
	if err != nil || !sched2 {
		t.Fatalf("expected page2 to schedule, got %v", err)
	}
	item2, _ := mgr.GetStatus(norm2)
	if item2.Sequence != 2 {
		t.Fatalf("expected sequence 2 for second unique item, got %d", item2.Sequence)
	}

	// Mark in progress
	if err := mgr.MarkInProgress(norm); err != nil {
		t.Fatalf("MarkInProgress failed: %v", err)
	}
	item, _ = mgr.GetStatus(norm)
	if item.Status != StatusInProgress {
		t.Fatalf("expected StatusInProgress, got %s", item.Status)
	}

	// Mark completed with metadata
	if err := mgr.MarkCompletedWithMetadata(norm, 200, 1024, 150*time.Millisecond, "data/pages/page1.html"); err != nil {
		t.Fatalf("MarkCompleted failed: %v", err)
	}
	item, _ = mgr.GetStatus(norm)
	if item.Status != StatusCompleted || item.StatusCode != 200 {
		t.Fatalf("expected StatusCompleted with 200, got %+v", item)
	}
	if item.Size != 1024 || item.DurationMs != 150 || item.SavedPath != "data/pages/page1.html" {
		t.Fatalf("metadata mismatch: %+v", item)
	}

	// Check stats
	stats := mgr.GetStats()
	if stats.TotalDiscovered != 2 || stats.Completed != 1 || stats.Pending != 1 {
		t.Fatalf("unexpected stats: %+v", stats)
	}
}

func TestURLManager_SnapshotConsistencyAndIndependence(t *testing.T) {
	mgr := NewManager()

	_, norm1, _ := mgr.TrySchedule("https://example.com/a", 0)
	_, norm2, _ := mgr.TrySchedule("https://example.com/b", 1)
	_, _, _ = mgr.TrySchedule("https://example.com/c", 1)

	_ = mgr.MarkInProgress(norm1)
	_ = mgr.MarkCompletedWithMetadata(norm1, 200, 500, 50*time.Millisecond, "a.html")
	_ = mgr.MarkInProgress(norm2)

	// Take snapshot
	snap := mgr.Snapshot()

	// 1. Verify snapshot counts
	if snap.Stats.TotalDiscovered != 3 {
		t.Errorf("Stats.TotalDiscovered = %d, want 3", snap.Stats.TotalDiscovered)
	}
	if snap.Stats.Completed != 1 || snap.Stats.InProgress != 1 || snap.Stats.Pending != 1 {
		t.Errorf("Stats breakdown mismatch: %+v", snap.Stats)
	}
	if len(snap.URLs) != 3 {
		t.Fatalf("URLs length = %d, want 3", len(snap.URLs))
	}
	if snap.LastSequence != 3 {
		t.Errorf("LastSequence = %d, want 3", snap.LastSequence)
	}

	// 2. Verify deterministic sequence order
	for i := 0; i < len(snap.URLs); i++ {
		expectedSeq := int64(i + 1)
		if snap.URLs[i].Sequence != expectedSeq {
			t.Errorf("URLs[%d].Sequence = %d, want %d", i, snap.URLs[i].Sequence, expectedSeq)
		}
	}

	// 3. Verify deep copy independence: Mutating snapshot must NOT affect original manager
	snap.URLs[0].Status = StatusFailed
	snap.URLs[0].ErrorMessage = "tampered"

	origItem, exists := mgr.GetStatus(norm1)
	if !exists {
		t.Fatal("norm1 missing from manager")
	}
	if origItem.Status != StatusCompleted || origItem.ErrorMessage != "" {
		t.Errorf("Snapshot mutation leaked into original URLManager: %+v", origItem)
	}

	// 4. Mutating manager after snapshot must not alter the captured snapshot
	_ = mgr.MarkCompleted(norm2, 200)
	if snap.Stats.Completed != 1 {
		t.Errorf("Manager mutation leaked into previously captured snapshot stats")
	}
	if snap.URLs[1].Status != StatusInProgress {
		t.Errorf("Manager mutation leaked into previously captured snapshot URLs")
	}
}

// TestURLManager_ConcurrentTrySchedule tests mutual exclusion under heavy concurrent contention.
// Verifies no race conditions, exactly 1 winner per unique URL, and sequence numbers remain dense and unique.
func TestURLManager_ConcurrentTrySchedule(t *testing.T) {
	mgr := NewManager()

	const numGoroutines = 50
	const numUniqueURLs = 10
	const attemptsPerGoroutine = 40

	var scheduledCounts [numUniqueURLs]int64
	var totalSuccessfulSchedules int64

	var wg sync.WaitGroup
	wg.Add(numGoroutines)

	startBarrier := make(chan struct{})

	for i := 0; i < numGoroutines; i++ {
		go func(workerID int) {
			defer wg.Done()
			<-startBarrier

			for j := 0; j < attemptsPerGoroutine; j++ {
				urlIndex := (workerID + j) % numUniqueURLs
				testURL := fmt.Sprintf("HTTP://Example.COM/page%d#fragment-%d", urlIndex, j)

				scheduled, _, err := mgr.TrySchedule(testURL, 1)
				if err != nil {
					t.Errorf("TrySchedule unexpected error: %v", err)
					return
				}

				if scheduled {
					atomic.AddInt64(&scheduledCounts[urlIndex], 1)
					atomic.AddInt64(&totalSuccessfulSchedules, 1)
				}
			}
		}(i)
	}

	close(startBarrier)
	wg.Wait()

	for i := 0; i < numUniqueURLs; i++ {
		wins := atomic.LoadInt64(&scheduledCounts[i])
		if wins != 1 {
			t.Fatalf("URL index %d was scheduled %d times, expected exactly 1", i, wins)
		}
	}

	if totalSuccessfulSchedules != numUniqueURLs {
		t.Fatalf("Total successful schedules = %d, expected %d", totalSuccessfulSchedules, numUniqueURLs)
	}

	snap := mgr.Snapshot()
	if snap.Stats.TotalDiscovered != numUniqueURLs {
		t.Fatalf("Snapshot TotalDiscovered = %d, expected %d", snap.Stats.TotalDiscovered, numUniqueURLs)
	}
	if snap.LastSequence != numUniqueURLs {
		t.Fatalf("Snapshot LastSequence = %d, expected %d", snap.LastSequence, numUniqueURLs)
	}

	// Verify all sequence numbers from 1 to numUniqueURLs are present and distinct
	seenSeqs := make(map[int64]bool)
	for _, u := range snap.URLs {
		if u.Sequence < 1 || u.Sequence > numUniqueURLs {
			t.Errorf("Unexpected sequence %d", u.Sequence)
		}
		if seenSeqs[u.Sequence] {
			t.Errorf("Duplicate sequence %d encountered", u.Sequence)
		}
		seenSeqs[u.Sequence] = true
	}
}
