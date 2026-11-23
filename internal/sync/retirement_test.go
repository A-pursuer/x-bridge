package sync

import (
	"context"
	"encoding/json"
	"github.com/xboard-bridge/xboard-xui-bridge/internal/config"
	"github.com/xboard-bridge/xboard-xui-bridge/internal/store"
	"github.com/xboard-bridge/xboard-xui-bridge/internal/xboard"
	"github.com/xboard-bridge/xboard-xui-bridge/internal/xui"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

func TestRetirementPreservesFinalDeltaAndDoesNotBlindlyResend(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "accepted", true: "uncertain"}[fail], func(t *testing.T) {
			ctx := context.Background()
			st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "bridge.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			email := "xboard_11111111_1"
			_, err = st.EnsureBaseline(ctx, "b", email, 4102)
			if err != nil {
				t.Fatal(err)
			}
			deleted, pushes, disabled := 0, 0, false
			xs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/panel/api/clients/update/" + email:
					var v map[string]any
					json.NewDecoder(r.Body).Decode(&v)
					disabled = v["enable"] == false
					io.WriteString(w, `{"success":true,"obj":true}`)
				case "/panel/api/inbounds/list":
					io.WriteString(w, `{"success":true,"obj":[{"id":1,"clientStats":[{"email":"`+email+`","up":100,"down":200}]}]}`)
				case "/panel/api/clients/del/" + email:
					deleted++
					io.WriteString(w, `{"success":true,"obj":true}`)
				default:
					http.NotFound(w, r)
				}
			}))
			defer xs.Close()
			bs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/v2/server/traffic/capabilities" {
					io.WriteString(w, `{"data":{"protocol":"traffic-batch-v1","ack":"database-committed","max_entries":1000}}`)
					return
				}
				if r.Method == "GET" {
					json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"state": "missing", "source_id": r.URL.Query().Get("source_id"), "seq": 1, "batch_id": r.URL.Query().Get("batch_id")}})
					return
				}
				pushes++
				var b xboard.TrafficBatch
				json.NewDecoder(r.Body).Decode(&b)
				if len(b.Entries) != 1 || b.Entries[0].SubscriptionID != 4102 || b.Entries[0].Up != 100 || b.Entries[0].Down != 200 {
					t.Errorf("wrong batch %#v", b)
				}
				if fail {
					w.WriteHeader(500)
					return
				}
				json.NewEncoder(w).Encode(map[string]any{"data": xboard.TrafficReceipt{State: "processed", SourceID: b.SourceID, Seq: b.Seq, BatchID: b.BatchID, PayloadHash: b.Hash(1), ProcessedAt: 1, RawUp: 100, RawDown: 200}})
			}))
			defer bs.Close()
			log := slog.New(slog.NewTextHandler(io.Discard, nil))
			xc, err := xui.New(config.Xui{APIHost: xs.URL, APIToken: "test", TimeoutSec: 2}, log)
			if err != nil {
				t.Fatal(err)
			}
			worker := &bridgeWorker{cfg: config.Bridge{Name: "b", XuiInboundID: 1, XboardNodeID: 1, XboardNodeType: "vless"}, store: st, xuiC: xc, xboardC: xboard.New(config.Xboard{APIHost: bs.URL, Token: "test", TimeoutSec: 2}), log: log}
			existing := map[string]xui.ClientSettings{email: {Email: email, Enable: true}}
			step := func() {
				t.Helper()
				m := map[string]struct{}{email: {}}
				if e := worker.retireClients(ctx, m, existing); e != nil && !fail {
					t.Fatal(e)
				}
			}
			step()
			if !disabled {
				t.Fatal("client was not disabled")
			}
			existing[email] = xui.ClientSettings{Email: email, Enable: false}
			rows, _ := st.ListRetirements(ctx, "b")
			r := rows[0]
			r.UpdatedAt = time.Now().Unix() - 20
			st.SaveRetirement(ctx, r)
			step()
			step()
			step()
			step()
			if (!fail && pushes != 1) || (fail && pushes < 1) {
				t.Fatalf("push count %d", pushes)
			}
			if fail {
				if deleted != 0 {
					t.Fatal("uncertain report destroyed counters")
				}
				rows, _ = st.ListRetirements(ctx, "b")
				if rows[0].State != "batch_pending" {
					t.Fatal(rows)
				}
			} else {
				if deleted != 1 {
					t.Fatalf("delete count %d", deleted)
				}
			}
		})
	}
}

func TestGeneratedSubIDDoesNotCausePerpetualUpdates(t *testing.T) {
	if needsUpdate(xui.ClientSettings{Enable: true, SubID: "panel-generated"}, xui.ClientSettings{Enable: true}) {
		t.Fatal("server-generated subID must be preserved")
	}
}
