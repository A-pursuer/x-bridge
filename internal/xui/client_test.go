package xui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/xboard-bridge/xboard-xui-bridge/internal/config"
)

func TestGetClientIPsDecodesSupportedShapes(t *testing.T) {
	tests := []struct {
		name string
		obj  string
		want []string
	}{
		{
			name: "legacy string array",
			obj:  `["1.1.1.1 (2026-07-16 12:00:00)","2.2.2.2"]`,
			want: []string{"1.1.1.1 (2026-07-16 12:00:00)", "2.2.2.2"},
		},
		{
			name: "current object array",
			obj:  `[{"ip":"1.1.1.1","time":"2026-07-16 12:00:00","node":"edge-1"},{"ip":"2.2.2.2","time":"","node":""}]`,
			want: []string{"1.1.1.1", "2.2.2.2"},
		},
		{
			name: "mixed array",
			obj:  `["1.1.1.1 (2026-07-16 12:00:00)",{"ip":"2.2.2.2","time":"","node":""}]`,
			want: []string{"1.1.1.1 (2026-07-16 12:00:00)", "2.2.2.2"},
		},
		{
			name: "empty array",
			obj:  `[]`,
			want: []string{},
		},
		{
			name: "object array skips empty ip",
			obj:  `[{"ip":"","time":"","node":""},{"ip":"2.2.2.2","time":"","node":""}]`,
			want: []string{"2.2.2.2"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newTestClient(t, tt.obj)
			got, err := c.GetClientIPs(context.Background(), "user@example.com")
			if err != nil {
				t.Fatalf("GetClientIPs returned error: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("GetClientIPs = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestGetClientIPsDecodesNoIPShapes(t *testing.T) {
	tests := []struct {
		name string
		obj  string
	}{
		{name: "null", obj: `null`},
		{name: "no ip record", obj: `"No IP Record"`},
		{name: "missing obj", obj: ``},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newTestClient(t, tt.obj)
			got, err := c.GetClientIPs(context.Background(), "user@example.com")
			if err != nil {
				t.Fatalf("GetClientIPs returned error: %v", err)
			}
			if got != nil {
				t.Fatalf("GetClientIPs = %#v, want nil", got)
			}
		})
	}
}

func TestGetClientIPsRejectsUnexpectedShapes(t *testing.T) {
	tests := []struct {
		name    string
		obj     string
		wantErr string
	}{
		{name: "unexpected string", obj: `"No records"`, wantErr: "意外字符串"},
		{name: "top-level object", obj: `{"ip":"1.1.1.1"}`, wantErr: "既非数组也非"},
		{name: "array number item", obj: `[1]`, wantErr: "既非字符串也非对象"},
		{name: "array bool item", obj: `[true]`, wantErr: "既非字符串也非对象"},
		{name: "array null item", obj: `[null]`, wantErr: "既非字符串也非对象"},
		{name: "object missing ip", obj: `[{"time":"2026-07-16 12:00:00","node":"edge-1"}]`, wantErr: "缺少 ip 字段"},
		{name: "object non-string ip", obj: `[{"ip":123,"time":"","node":""}]`, wantErr: "ip 字段无效"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newTestClient(t, tt.obj)
			_, err := c.GetClientIPs(context.Background(), "user@example.com")
			if err == nil {
				t.Fatal("GetClientIPs returned nil error")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("GetClientIPs error = %q, want containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestGetOnlinesContract(t *testing.T) {
	c := newRoutedTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		requireRequest(t, r, http.MethodPost, "/panel/api/clients/onlines")
		_, _ = w.Write([]byte(`{"success":true,"msg":"","obj":["a@example.com","b@example.com"]}`))
	})
	got, err := c.GetOnlines(context.Background())
	if err != nil {
		t.Fatalf("GetOnlines: %v", err)
	}
	want := []string{"a@example.com", "b@example.com"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("GetOnlines = %#v, want %#v", got, want)
	}
}

func TestGetClientIPsByGuidContract(t *testing.T) {
	c := newRoutedTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		requireRequest(t, r, http.MethodPost, "/panel/api/clients/clientIpsByGuid")
		_, _ = w.Write([]byte(`{"success":true,"msg":"","obj":{"guid-a":{"u@x":[{"ip":"1.1.1.1","timestamp":20},{"ip":"2.2.2.2","timestamp":10}]}}}`))
	})
	got, err := c.GetClientIPsByGuid(context.Background())
	if err != nil {
		t.Fatalf("GetClientIPsByGuid: %v", err)
	}
	want := map[string]map[string][]ClientIpEntry{
		"guid-a": {"u@x": {{IP: "1.1.1.1", Timestamp: 20}, {IP: "2.2.2.2", Timestamp: 10}}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("GetClientIPsByGuid = %#v, want %#v", got, want)
	}
}

func TestGetPanelGuidCachesServerStatus(t *testing.T) {
	calls := 0
	c := newRoutedTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		requireRequest(t, r, http.MethodGet, "/panel/api/server/status")
		calls++
		_, _ = w.Write([]byte(`{"success":true,"msg":"","obj":{"panelGuid":"panel-guid","cpu":1,"mem":{"current":1,"total":2},"swap":{"current":0,"total":0},"disk":{"current":3,"total":4}}}`))
	})
	for i := 0; i < 2; i++ {
		got, err := c.GetPanelGuid(context.Background())
		if err != nil {
			t.Fatalf("GetPanelGuid call %d: %v", i, err)
		}
		if got != "panel-guid" {
			t.Fatalf("GetPanelGuid = %q", got)
		}
	}
	if calls != 1 {
		t.Fatalf("server status calls = %d, want 1", calls)
	}
}

func TestAddClientBulkCreateContract(t *testing.T) {
	c := newRoutedTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		requireRequest(t, r, http.MethodPost, "/panel/api/clients/bulkCreate")
		if got := r.URL.Query().Encode(); got != "" {
			t.Fatalf("query = %q", got)
		}
		_, _ = w.Write([]byte(`{"success":true,"msg":"","obj":{"created":1}}`))
	})
	err := c.AddClient(context.Background(), 7, []ClientSettings{{
		Email: "u@x", ID: "uuid", Enable: true, LimitIP: 2,
	}})
	if err != nil {
		t.Fatalf("AddClient: %v", err)
	}
}

func TestAddClientBulkCreateSkippedFails(t *testing.T) {
	c := newRoutedTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		requireRequest(t, r, http.MethodPost, "/panel/api/clients/bulkCreate")
		_, _ = w.Write([]byte(`{"success":true,"msg":"","obj":{"created":0,"skipped":[{"email":"u@x","reason":"email already in use"}]}}`))
	})
	err := c.AddClient(context.Background(), 7, []ClientSettings{{Email: "u@x", ID: "uuid"}})
	if err == nil || !strings.Contains(err.Error(), "email already in use") {
		t.Fatalf("AddClient err = %v", err)
	}
}

func newTestClient(t *testing.T, obj string) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requireRequest(t, r, http.MethodPost, "/panel/api/clients/ips/user@example.com")
		w.Header().Set("Content-Type", "application/json")
		if obj == "" {
			_, _ = w.Write([]byte(`{"success":true,"msg":""}`))
			return
		}
		_, _ = w.Write([]byte(`{"success":true,"msg":"","obj":` + obj + `}`))
	}))
	t.Cleanup(srv.Close)

	c, err := New(config.Xui{
		APIHost:    srv.URL,
		APIToken:   "test-token",
		TimeoutSec: 5,
	}, nil)
	if err != nil {
		t.Fatalf("New xui client: %v", err)
	}
	return c
}

func newRoutedTestClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	c, err := New(config.Xui{
		APIHost:    srv.URL,
		APIToken:   "test-token",
		TimeoutSec: 5,
	}, nil)
	if err != nil {
		t.Fatalf("New xui client: %v", err)
	}
	return c
}

func requireRequest(t *testing.T, r *http.Request, method, path string) {
	t.Helper()
	if r.Method != method {
		t.Fatalf("method = %s, want %s", r.Method, method)
	}
	if r.URL.Path != path {
		t.Fatalf("path = %s, want %s", r.URL.Path, path)
	}
	if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
		t.Fatalf("Authorization = %q", got)
	}
	if got := r.Header.Get("Accept"); got != "application/json" {
		t.Fatalf("Accept = %q", got)
	}
}
