package main

import (
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func adminStub(t *testing.T, code int) string {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health/ready" {
			t.Errorf("path %q", r.URL.Path)
		}
		w.WriteHeader(code)
	}))
	t.Cleanup(srv.Close)
	return ":" + srv.URL[strings.LastIndex(srv.URL, ":")+1:]
}

func TestHealthcheck(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	closed := ":" + strings.Split(ln.Addr().String(), ":")[1]
	ln.Close()
	for name, tc := range map[string]struct {
		addr string
		want int
	}{
		"ready":       {adminStub(t, http.StatusOK), 0},
		"draining":    {adminStub(t, http.StatusServiceUnavailable), 1},
		"port closed": {closed, 1},
	} {
		t.Run(name, func(t *testing.T) {
			getenv := func(k string) string {
				if k == "ADMIN_ADDR" {
					return tc.addr
				}
				return ""
			}
			if got := healthcheck(getenv); got != tc.want {
				t.Fatalf("exit %d, want %d", got, tc.want)
			}
		})
	}
}

func TestHealthcheckDefaultsAdminAddr(t *testing.T) {
	if got := adminURL(func(string) string { return "" }); got != "http://127.0.0.1:9090/health/ready" {
		t.Fatalf("got %q", got)
	}
}
