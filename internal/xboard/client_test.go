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

func TestMultiSubscriptionUIDsRemainIndependentAcrossFetchAndTrafficPush(t *testing.T) {
	const (
		streamingSubscriptionID = int64(4101)
		broadbandSubscriptionID = int64(4102)
	)
	var gotTraffic PushTraffic

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/server/user":
			requireXboardRequest(t, r, http.MethodGet, "/api/v2/server/user")
			w.Header().Set("ETag", `"multi-subscription"`)
			_, _ = w.Write([]byte(`{"users":[{"id":4101,"uuid":"11111111-1111-4111-8111-111111111111","speed_limit":0,"device_limit":2},{"id":4102,"uuid":"22222222-2222-4222-8222-222222222222","speed_limit":100,"device_limit":4}]}`))
		case "/api/v2/server/push":
			requireXboardRequest(t, r, http.MethodPost, "/api/v2/server/push")
			if err := json.NewDecoder(r.Body).Decode(&gotTraffic); err != nil {
				t.Fatalf("decode traffic: %v", err)
			}
			_, _ = w.Write([]byte(`{"data":true}`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	t.Cleanup(srv.Close)

	c := New(config.Xboard{APIHost: srv.URL, Token: "server-token", TimeoutSec: 5})
	users, err := c.FetchUsers(context.Background(), 7, "vless")
	if err != nil {
		t.Fatalf("FetchUsers: %v", err)
	}
	if len(users.Users) != 2 || users.Users[0].ID != streamingSubscriptionID || users.Users[1].ID != broadbandSubscriptionID {
		t.Fatalf("subscription UIDs = %#v", users.Users)
	}
	if users.ETag != `"multi-subscription"` {
		t.Fatalf("ETag = %q", users.ETag)
	}

	traffic := PushTraffic{}
	traffic.Set(streamingSubscriptionID, 101, 201)
	traffic.Set(broadbandSubscriptionID, 102, 202)
	if err := c.PushTraffic(context.Background(), 7, "vless", traffic); err != nil {
		t.Fatalf("PushTraffic: %v", err)
	}
	if !reflect.DeepEqual(gotTraffic, traffic) {
		t.Fatalf("traffic = %#v, want %#v", gotTraffic, traffic)
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
	if method != http.MethodGet && r.Header.Get("Content-Type") != "application/json" {
		got := r.Header.Get("Content-Type")
		t.Fatalf("Content-Type = %q", got)
	}
}
