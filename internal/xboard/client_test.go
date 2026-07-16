package xboard

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/xboard-bridge/xboard-xui-bridge/internal/config"
)

func TestPushReportContractAliveAndStatus(t *testing.T) {
	var gotBody map[string]json.RawMessage
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requireXboardRequest(t, r, http.MethodPost, "/api/v2/server/report")
		if got := r.URL.Query().Get("token"); got != "server-token" {
			t.Fatalf("token = %q", got)
		}
		if got := r.URL.Query().Get("node_id"); got != "7" {
			t.Fatalf("node_id = %q", got)
		}
		if got := r.URL.Query().Get("node_type"); got != "vless" {
			t.Fatalf("node_type = %q", got)
		}
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		_, _ = w.Write([]byte(`{"data":true}`))
	}))
	t.Cleanup(srv.Close)

	c := New(config.Xboard{
		APIHost:    srv.URL,
		Token:      "server-token",
		TimeoutSec: 5,
		UserAgent:  "test-agent",
	})
	report := Report{
		Alive: AliveMap{"11": {"1.1.1.1", "2.2.2.2"}},
		Status: &StatusReport{
			CPU:  12.5,
			Mem:  StatusPair{Total: 100, Used: 40},
			Swap: StatusPair{Total: 10, Used: 1},
			Disk: StatusPair{Total: 200, Used: 80},
		},
	}
	if err := c.PushReport(context.Background(), 7, "vless", report); err != nil {
		t.Fatalf("PushReport: %v", err)
	}

	if _, ok := gotBody["traffic"]; ok {
		t.Fatal("report body unexpectedly contains traffic")
	}
	var alive AliveMap
	if err := json.Unmarshal(gotBody["alive"], &alive); err != nil {
		t.Fatalf("decode alive: %v", err)
	}
	if !reflect.DeepEqual(alive, report.Alive) {
		t.Fatalf("alive = %#v, want %#v", alive, report.Alive)
	}
	var status StatusReport
	if err := json.Unmarshal(gotBody["status"], &status); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if !reflect.DeepEqual(status, *report.Status) {
		t.Fatalf("status = %#v, want %#v", status, *report.Status)
	}
}

func TestPushReportRejectsDataFalse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requireXboardRequest(t, r, http.MethodPost, "/api/v2/server/report")
		_, _ = w.Write([]byte(`{"data":false,"message":"bad report"}`))
	}))
	t.Cleanup(srv.Close)

	c := New(config.Xboard{APIHost: srv.URL, Token: "server-token", TimeoutSec: 5})
	err := c.PushReport(context.Background(), 7, "vless", Report{Alive: AliveMap{"11": {"1.1.1.1"}}})
	if err == nil || !strings.Contains(err.Error(), "bad report") {
		t.Fatalf("PushReport err = %v", err)
	}
}

func requireXboardRequest(t *testing.T, r *http.Request, method, path string) {
	t.Helper()
	if r.Method != method {
		t.Fatalf("method = %s, want %s", r.Method, method)
	}
	if r.URL.Path != path {
		t.Fatalf("path = %s, want %s", r.URL.Path, path)
	}
	if got := r.Header.Get("Accept"); got != "application/json" {
		t.Fatalf("Accept = %q", got)
	}
	if got := r.Header.Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q", got)
	}
}
