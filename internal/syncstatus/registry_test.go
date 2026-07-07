package syncstatus

import (
	"sync"
	"testing"
	"time"
)

func TestRecordAndSnapshotFor(t *testing.T) {
	r := New()
	r.Record("br-a", "user_sync", true, "", 10*time.Millisecond)
	r.Record("br-a", "traffic_sync", false, "boom", 20*time.Millisecond)
	r.Record("br-b", "user_sync", true, "", 5*time.Millisecond)

	// 只请求 br-a：应拿到它的两条 loop，按 loop 名排序（traffic 在 user 前）。
	snap := r.SnapshotFor([]string{"br-a"})
	loops, ok := snap["br-a"]
	if !ok {
		t.Fatalf("期望 br-a 存在于快照")
	}
	if len(loops) != 2 {
		t.Fatalf("期望 br-a 有 2 条 loop，实际 %d", len(loops))
	}
	if loops[0].Loop != "traffic_sync" || loops[1].Loop != "user_sync" {
		t.Fatalf("loop 未按名排序：%q, %q", loops[0].Loop, loops[1].Loop)
	}
	if loops[0].OK || loops[0].ErrMsg != "boom" {
		t.Fatalf("traffic_sync 应为失败且带 err，实际 ok=%v msg=%q", loops[0].OK, loops[0].ErrMsg)
	}
	if !loops[1].OK || loops[1].ElapsedMs != 10 {
		t.Fatalf("user_sync 应成功且 elapsed=10ms，实际 ok=%v ms=%d", loops[1].OK, loops[1].ElapsedMs)
	}
	// 未请求 br-b：不应出现。
	if _, exists := snap["br-b"]; exists {
		t.Fatalf("br-b 未被请求，不应出现在快照")
	}
}

func TestSnapshotForFiltersDeletedBridge(t *testing.T) {
	r := New()
	r.Record("gone", "user_sync", true, "", time.Millisecond)
	r.Record("alive", "user_sync", true, "", time.Millisecond)

	// 只传当前存在的 "alive"，已删的 "gone" 应被天然过滤。
	snap := r.SnapshotFor([]string{"alive"})
	if _, exists := snap["gone"]; exists {
		t.Fatalf("已删桥接 gone 不应出现在快照")
	}
	if _, exists := snap["alive"]; !exists {
		t.Fatalf("alive 应出现在快照")
	}
}

func TestRecordOverwritesLatest(t *testing.T) {
	r := New()
	r.Record("br", "user_sync", false, "old", time.Millisecond)
	r.Record("br", "user_sync", true, "", 2*time.Millisecond)
	loops := r.SnapshotFor([]string{"br"})["br"]
	if len(loops) != 1 {
		t.Fatalf("同一 loop 应只保留最新一条，实际 %d 条", len(loops))
	}
	if !loops[0].OK {
		t.Fatalf("应保留最新的成功状态，实际 ok=%v", loops[0].OK)
	}
}

// TestConcurrentRecord 在 -race 下验证并发写入无数据竞争。
func TestConcurrentRecord(t *testing.T) {
	r := New()
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			r.Record("br", "user_sync", true, "", time.Duration(n)*time.Millisecond)
			_ = r.SnapshotFor([]string{"br"})
		}(i)
	}
	wg.Wait()
	if loops := r.SnapshotFor([]string{"br"})["br"]; len(loops) != 1 {
		t.Fatalf("并发写后应仍只有 1 条 loop，实际 %d", len(loops))
	}
}
