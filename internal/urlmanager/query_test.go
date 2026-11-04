package urlmanager

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestURLManager_GetBySequence(t *testing.T) {
	mgr := NewManager()

	// 1. Non-positive sequence lookup returns false immediately
	if item, ok := mgr.GetBySequence(0); ok || item != nil {
		t.Errorf("expected sequence 0 to return false and nil, got ok=%t, item=%+v", ok, item)
	}
	if item, ok := mgr.GetBySequence(-5); ok || item != nil {
		t.Errorf("expected negative sequence to return false and nil, got ok=%t, item=%+v", ok, item)
	}

	// 2. Schedule URLs
	_, norm1, err := mgr.TrySchedule("https://example.com/item1", 0)
	if err != nil {
		t.Fatalf("TrySchedule 1 failed: %v", err)
	}
	_, _, err = mgr.TrySchedule("https://example.com/item2", 1)
	if err != nil {
		t.Fatalf("TrySchedule 2 failed: %v", err)
	}

	_ = mgr.MarkInProgress(norm1)
	_ = mgr.MarkCompletedWithMetadata(norm1, 200, 1024, 50*time.Millisecond, "data/pages/item1.html")

	// 3. Lookup existing sequence 1
	item1, ok := mgr.GetBySequence(1)
	if !ok || item1 == nil {
		t.Fatalf("expected sequence 1 to exist")
	}
	if item1.Sequence != 1 || item1.Status != StatusCompleted || item1.StatusCode != 200 {
		t.Errorf("sequence 1 metadata mismatch: %+v", item1)
	}

	// 4. Lookup existing sequence 2
	item2, ok := mgr.GetBySequence(2)
	if !ok || item2 == nil {
		t.Fatalf("expected sequence 2 to exist")
	}
	if item2.Sequence != 2 || item2.Status != StatusPending || item2.Depth != 1 {
		t.Errorf("sequence 2 metadata mismatch: %+v", item2)
	}

	// 5. Lookup non-existent sequence
	if item, ok := mgr.GetBySequence(99); ok || item != nil {
		t.Errorf("expected sequence 99 to return false, got ok=%t, item=%+v", ok, item)
	}

	// 6. Data isolation: mutating returned item does not corrupt internal state
	item1.Status = StatusFailed
	item1.StatusCode = 500

	freshItem1, ok := mgr.GetBySequence(1)
	if !ok || freshItem1.Status != StatusCompleted || freshItem1.StatusCode != 200 {
		t.Errorf("internal state was modified through returned copy! got %+v", freshItem1)
	}
}

func TestURLManager_GetFilteredURLs_Pagination(t *testing.T) {
	mgr := NewManager()

	// Schedule 15 items (sequences 1 to 15)
	for i := 1; i <= 15; i++ {
		urlStr := fmt.Sprintf("https://example.com/page/%d", i)
		_, _, err := mgr.TrySchedule(urlStr, i%3)
		if err != nil {
			t.Fatalf("TrySchedule %d failed: %v", i, err)
		}
	}

	// 1. Page 1: limit 5, offset 0
	items, total, filtered := mgr.GetFilteredURLs(URLFilterOptions{
		Offset: 0,
		Limit:  5,
	})
	if total != 15 || filtered != 15 {
		t.Errorf("total=%d (want 15), filtered=%d (want 15)", total, filtered)
	}
	if len(items) != 5 {
		t.Fatalf("page 1 len=%d (want 5)", len(items))
	}
	for i, it := range items {
		expectedSeq := int64(i + 1)
		if it.Sequence != expectedSeq {
			t.Errorf("page 1 item[%d] sequence=%d (want %d)", i, it.Sequence, expectedSeq)
		}
	}

	// 2. Page 2: limit 5, offset 5
	items2, total2, filtered2 := mgr.GetFilteredURLs(URLFilterOptions{
		Offset: 5,
		Limit:  5,
	})
	if total2 != 15 || filtered2 != 15 || len(items2) != 5 {
		t.Fatalf("page 2 unexpected: total=%d, filtered=%d, len=%d", total2, filtered2, len(items2))
	}
	for i, it := range items2 {
		expectedSeq := int64(i + 6)
		if it.Sequence != expectedSeq {
			t.Errorf("page 2 item[%d] sequence=%d (want %d)", i, it.Sequence, expectedSeq)
		}
	}

	// 3. Partial final page: limit 5, offset 12 (items 13, 14, 15)
	items3, _, _ := mgr.GetFilteredURLs(URLFilterOptions{
		Offset: 12,
		Limit:  5,
	})
	if len(items3) != 3 {
		t.Fatalf("page 3 len=%d (want 3)", len(items3))
	}
	if items3[0].Sequence != 13 || items3[1].Sequence != 14 || items3[2].Sequence != 15 {
		t.Errorf("page 3 items mismatch: %+v", items3)
	}

	// 4. Offset beyond filtered count
	itemsBeyond, totalB, filteredB := mgr.GetFilteredURLs(URLFilterOptions{
		Offset: 25,
		Limit:  10,
	})
	if len(itemsBeyond) != 0 || totalB != 15 || filteredB != 15 {
		t.Errorf("expected empty slice for offset beyond count, got len=%d", len(itemsBeyond))
	}

	// 5. Default limit normalization (limit <= 0 defaults to 20; limit > 100 clamps to 100)
	itemsDefault, _, _ := mgr.GetFilteredURLs(URLFilterOptions{
		Offset: 0,
		Limit:  0,
	})
	if len(itemsDefault) != 15 {
		t.Errorf("expected 15 items returned with default limit 20, got %d", len(itemsDefault))
	}

	// 6. Negative offset normalization
	itemsNegOffset, _, _ := mgr.GetFilteredURLs(URLFilterOptions{
		Offset: -10,
		Limit:  5,
	})
	if len(itemsNegOffset) != 5 || itemsNegOffset[0].Sequence != 1 {
		t.Errorf("expected negative offset to normalize to 0, got len=%d, seq=%d", len(itemsNegOffset), itemsNegOffset[0].Sequence)
	}
}

func TestURLManager_GetFilteredURLs_Filtering(t *testing.T) {
	mgr := NewManager()

	// Create a controlled dataset:
	// Item 1: completed, depth 0
	// Item 2: completed, depth 1
	// Item 3: pending,   depth 1
	// Item 4: in_prog,   depth 2
	// Item 5: failed,    depth 2
	_, n1, _ := mgr.TrySchedule("https://example.com/1", 0)
	_, n2, _ := mgr.TrySchedule("https://example.com/2", 1)
	_, _, _ = mgr.TrySchedule("https://example.com/3", 1)
	_, n4, _ := mgr.TrySchedule("https://example.com/4", 2)
	_, n5, _ := mgr.TrySchedule("https://example.com/5", 2)

	_ = mgr.MarkInProgress(n1)
	_ = mgr.MarkCompletedWithMetadata(n1, 200, 100, time.Millisecond, "1.html")

	_ = mgr.MarkInProgress(n2)
	_ = mgr.MarkCompletedWithMetadata(n2, 200, 200, time.Millisecond, "2.html")

	_ = mgr.MarkInProgress(n4)

	_ = mgr.MarkInProgress(n5)
	_ = mgr.MarkFailed(n5, 404, fmt.Errorf("not found"))

	// 1. Filter by Status = completed
	stCompleted := StatusCompleted
	itemsComp, total, filtered := mgr.GetFilteredURLs(URLFilterOptions{
		Status: &stCompleted,
	})
	if total != 5 || filtered != 2 || len(itemsComp) != 2 {
		t.Fatalf("StatusCompleted filter mismatch: total=%d, filtered=%d, len=%d", total, filtered, len(itemsComp))
	}
	for _, it := range itemsComp {
		if it.Status != StatusCompleted {
			t.Errorf("expected completed item, got %s", it.Status)
		}
	}

	// 2. Filter by Depth = 2
	depth2 := 2
	itemsDepth2, totalD, filteredD := mgr.GetFilteredURLs(URLFilterOptions{
		Depth: &depth2,
	})
	if totalD != 5 || filteredD != 2 || len(itemsDepth2) != 2 {
		t.Fatalf("Depth=2 filter mismatch: total=%d, filtered=%d, len=%d", totalD, filteredD, len(itemsDepth2))
	}
	for _, it := range itemsDepth2 {
		if it.Depth != 2 {
			t.Errorf("expected depth 2 item, got %d", it.Depth)
		}
	}

	// 3. Combined Filter: Status = completed AND Depth = 1
	depth1 := 1
	itemsComb, totalC, filteredC := mgr.GetFilteredURLs(URLFilterOptions{
		Status: &stCompleted,
		Depth:  &depth1,
	})
	if totalC != 5 || filteredC != 1 || len(itemsComb) != 1 {
		t.Fatalf("Combined filter mismatch: total=%d, filtered=%d, len=%d", totalC, filteredC, len(itemsComb))
	}
	if itemsComb[0].Sequence != 2 {
		t.Errorf("expected item sequence 2, got %d", itemsComb[0].Sequence)
	}

	// 4. Filter matching 0 items
	depth99 := 99
	itemsEmpty, totalE, filteredE := mgr.GetFilteredURLs(URLFilterOptions{
		Depth: &depth99,
	})
	if totalE != 5 || filteredE != 0 || len(itemsEmpty) != 0 {
		t.Fatalf("Empty filter mismatch: total=%d, filtered=%d, len=%d", totalE, filteredE, len(itemsEmpty))
	}
}

func TestURLManager_GetFilteredURLs_Sorting(t *testing.T) {
	mgr := NewManager()

	_, n1, _ := mgr.TrySchedule("https://example.com/a", 2)
	_, n2, _ := mgr.TrySchedule("https://example.com/b", 0)
	_, n3, _ := mgr.TrySchedule("https://example.com/c", 1)

	// Item 1 (seq 1, depth 2)
	// Item 2 (seq 2, depth 0)
	// Item 3 (seq 3, depth 1)
	_ = n1
	_ = n2
	_ = n3

	// 1. Sort seq_desc
	itemsDesc, _, _ := mgr.GetFilteredURLs(URLFilterOptions{
		SortBy: "seq_desc",
	})
	if len(itemsDesc) != 3 || itemsDesc[0].Sequence != 3 || itemsDesc[1].Sequence != 2 || itemsDesc[2].Sequence != 1 {
		t.Errorf("seq_desc sort failed: %+v", itemsDesc)
	}

	// 2. Sort depth_asc
	itemsDepthAsc, _, _ := mgr.GetFilteredURLs(URLFilterOptions{
		SortBy: "depth_asc",
	})
	if len(itemsDepthAsc) != 3 || itemsDepthAsc[0].Depth != 0 || itemsDepthAsc[1].Depth != 1 || itemsDepthAsc[2].Depth != 2 {
		t.Errorf("depth_asc sort failed: %+v", itemsDepthAsc)
	}

	// 3. Sort depth_desc
	itemsDepthDesc, _, _ := mgr.GetFilteredURLs(URLFilterOptions{
		SortBy: "depth_desc",
	})
	if len(itemsDepthDesc) != 3 || itemsDepthDesc[0].Depth != 2 || itemsDepthDesc[1].Depth != 1 || itemsDepthDesc[2].Depth != 0 {
		t.Errorf("depth_desc sort failed: %+v", itemsDepthDesc)
	}

	// 4. Sort time_desc
	time.Sleep(5 * time.Millisecond)
	_ = mgr.MarkInProgress(n2) // n2 updated now
	time.Sleep(5 * time.Millisecond)
	_ = mgr.MarkInProgress(n1) // n1 updated last (newest)

	itemsTimeDesc, _, _ := mgr.GetFilteredURLs(URLFilterOptions{
		SortBy: "time_desc",
	})
	if len(itemsTimeDesc) != 3 {
		t.Fatalf("time_desc len=%d (want 3)", len(itemsTimeDesc))
	}
	if itemsTimeDesc[0].Sequence != 1 || itemsTimeDesc[1].Sequence != 2 || itemsTimeDesc[2].Sequence != 3 {
		t.Errorf("time_desc order failed (expected seq 1, then 2, then 3): got seqs %d, %d, %d",
			itemsTimeDesc[0].Sequence, itemsTimeDesc[1].Sequence, itemsTimeDesc[2].Sequence)
	}
}

func TestURLManager_GetFilteredURLs_ConcurrentSafe(t *testing.T) {
	mgr := NewManager()

	const writerCount = 8
	const readerCount = 8
	const itemsPerWriter = 25

	var wg sync.WaitGroup
	wg.Add(writerCount + readerCount)

	// Writers: concurrently scheduling and transitioning URLs
	for w := 0; w < writerCount; w++ {
		workerID := w
		go func() {
			defer wg.Done()
			for i := 0; i < itemsPerWriter; i++ {
				u := fmt.Sprintf("https://example.com/worker-%d/item-%d", workerID, i)
				sched, norm, err := mgr.TrySchedule(u, i%3)
				if err == nil && sched {
					if i%2 == 0 {
						_ = mgr.MarkInProgress(norm)
						_ = mgr.MarkCompletedWithMetadata(norm, 200, 500, time.Millisecond, "page.html")
					}
				}
			}
		}()
	}

	// Readers: concurrently querying filtered and sequence-indexed URLs
	for r := 0; r < readerCount; r++ {
		go func() {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				// Query filtered URLs
				items, total, filtered := mgr.GetFilteredURLs(URLFilterOptions{
					Limit:  10,
					Offset: i % 5,
				})
				if len(items) > 10 {
					t.Errorf("limit exceeded: len=%d", len(items))
				}
				if filtered > total {
					t.Errorf("filtered (%d) > total (%d)", filtered, total)
				}

				// Query by sequence
				_, _ = mgr.GetBySequence(int64(i + 1))
			}
		}()
	}

	wg.Wait()

	stats := mgr.GetStats()
	if stats.TotalDiscovered != writerCount*itemsPerWriter {
		t.Errorf("TotalDiscovered = %d, want %d", stats.TotalDiscovered, writerCount*itemsPerWriter)
	}
}

