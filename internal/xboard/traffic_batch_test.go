package xboard

import (
	"context"
	"github.com/xboard-bridge/xboard-xui-bridge/internal/config"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTrafficTransportErrorDoesNotExposeQueryToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { c, _, _ := w.(http.Hijacker).Hijack(); c.Close() }))
	defer server.Close()
	c := New(config.Xboard{APIHost: server.URL, Token: "private-test-token", TimeoutSec: 2})
	e := c.TrafficCapabilities(context.Background(), 1, "vless")
	if e == nil || strings.Contains(e.Error(), "private-test-token") || strings.Contains(e.Error(), "token=") {
		t.Fatal("transport error leaked URL credentials", e)
	}
}
