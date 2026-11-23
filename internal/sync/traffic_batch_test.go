package sync

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/xboard-bridge/xboard-xui-bridge/internal/config"
	"github.com/xboard-bridge/xboard-xui-bridge/internal/store"
	"github.com/xboard-bridge/xboard-xui-bridge/internal/xboard"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

func TestCommittedResponseLostThenRestartReplaysSameBatch(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "b.db")
	st, _ := store.OpenSQLite(path)
	st.EnsureBaseline(ctx, "b", "e", 7)
	st.ObserveTraffic(ctx, "b", "e", 100, 200)
	receipts := map[string]xboard.TrafficReceipt{}
	posts := 0
	lose := true
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v2/server/traffic/capabilities" {
			fmt.Fprint(w, `{"data":{"protocol":"traffic-batch-v1","ack":"database-committed","max_entries":1000}}`)
			return
		}
		if r.Method == "GET" {
			id := r.URL.Query().Get("batch_id")
			if got, ok := receipts[id]; ok {
				json.NewEncoder(w).Encode(map[string]any{"data": got})
			} else {
				fmt.Fprintf(w, `{"data":{"state":"missing","source_id":%q,"seq":1,"batch_id":%q}}`, r.URL.Query().Get("source_id"), id)
			}
			return
		}
		posts++
		var b xboard.TrafficBatch
		json.NewDecoder(r.Body).Decode(&b)
		var u, d int64
		for _, e := range b.Entries {
			u += e.Up
			d += e.Down
		}
		got := xboard.TrafficReceipt{State: "processed", SourceID: b.SourceID, Seq: b.Seq, BatchID: b.BatchID, PayloadHash: b.Hash(1), ProcessedAt: 1, RawUp: u, RawDown: d}
		receipts[b.BatchID] = got
		if lose {
			lose = false
			conn, _, _ := w.(http.Hijacker).Hijack()
			conn.Close()
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"data": got})
	}))
	defer server.Close()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := config.Bridge{Name: "b", XboardNodeID: 1}
	w := &bridgeWorker{cfg: cfg, store: st, xboardC: xboard.New(config.Xboard{APIHost: server.URL, TimeoutSec: 2}), log: log}
	if e := w.flushTraffic(ctx, ""); e == nil {
		t.Fatal("expected lost response")
	}
	first, _ := st.PendingTrafficBatch(ctx, "b")
	st.Close()
	st, _ = store.OpenSQLite(path)
	defer st.Close()
	w.store = st
	if e := w.flushTraffic(ctx, ""); e != nil {
		t.Fatal(e)
	}
	if posts != 1 || len(receipts) != 1 {
		t.Fatal("duplicate commit", posts, len(receipts))
	}
	if _, e := st.PendingTrafficBatch(ctx, "b"); e != store.ErrNotFound {
		t.Fatal(e)
	}
	history, _ := st.TrafficBatchStatus(ctx, "b")
	if history[0].Batch.BatchID != first.Batch.BatchID || history[0].State != "processed" {
		t.Fatal(history)
	}
}
