package probe

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"

	netcupv1 "github.com/thorion3006/foip-operator/api/v1"
)

func TestAggregate(t *testing.T) {
	results := []Result{{Success: true}, {Success: false}}
	if got := Aggregate(netcupv1.ProbeCompositionAll, 0, results); got.Success {
		t.Fatal("all should fail when one result fails")
	}
	if got := Aggregate(netcupv1.ProbeCompositionAny, 0, results); !got.Success {
		t.Fatal("any should succeed with one successful result")
	}
	if got := Aggregate(netcupv1.ProbeCompositionQuorum, 2, results); got.Success {
		t.Fatal("quorum should fail below threshold")
	}
}

func TestTCPProbe(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		if os.IsPermission(err) || strings.Contains(err.Error(), "operation not permitted") {
			t.Skipf("network sockets unavailable in this environment: %v", err)
		}
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr == nil {
			_ = conn.Close()
		}
	}()
	host, port, _ := net.SplitHostPort(listener.Addr().String())
	var p int
	_, _ = fmt.Sscan(port, &p)
	result := Execute(context.Background(), netcupv1.FailoverProbeSpec{Phase: netcupv1.ProbePhasePreRoute, Type: netcupv1.ProbeTypeTCP, Target: netcupv1.ProbeTarget{Address: host, Port: int32(p)}, NetworkPolicy: netcupv1.ProbeNetworkPolicy{AllowPrivateNetworks: true}})
	if !result.Success {
		t.Fatalf("TCP probe failed: %s", result.Reason)
	}
}

func TestHTTPProbeMatchesMethodStatusBodyAndHeaders(t *testing.T) {
	var request *http.Request
	server := newLoopbackServer(t, "127.0.0.1", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		request = r
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, "service is ready")
	}))
	defer server.Close()

	target := targetFromServerURL(t, server.URL)
	target.Path = "/ready"
	target.Host = "service.example"
	result := Execute(context.Background(), netcupv1.FailoverProbeSpec{
		Phase:             netcupv1.ProbePhasePostRoute,
		Type:              netcupv1.ProbeTypeHTTP,
		Target:            target,
		Method:            http.MethodPost,
		ExpectedStatusMin: http.StatusCreated,
		ExpectedStatusMax: http.StatusCreated,
		BodyMatch:         "service is ready",
		Headers: []netcupv1.ProbeHeader{
			{Name: "X-Probe-Check", Value: "enabled"},
		},
		NetworkPolicy: netcupv1.ProbeNetworkPolicy{AllowPrivateNetworks: true},
	})
	if !result.Success {
		t.Fatalf("HTTP probe failed: %s", result.Reason)
	}
	if request == nil {
		t.Fatal("HTTP server did not receive a request")
	}
	if request.Method != http.MethodPost {
		t.Fatalf("method = %q, want POST", request.Method)
	}
	if request.URL.Path != "/ready" {
		t.Fatalf("path = %q, want /ready", request.URL.Path)
	}
	if request.Host != "service.example" {
		t.Fatalf("Host = %q, want service.example", request.Host)
	}
	if request.Header.Get("X-Probe-Check") != "enabled" {
		t.Fatalf("X-Probe-Check = %q, want enabled", request.Header.Get("X-Probe-Check"))
	}
}

func TestHTTPProbeAcceptsExplicitURLAddress(t *testing.T) {
	server := newLoopbackServer(t, "127.0.0.1", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	parsed, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil {
		t.Fatal(err)
	}
	result := Execute(context.Background(), netcupv1.FailoverProbeSpec{
		Phase:         netcupv1.ProbePhasePostRoute,
		Type:          netcupv1.ProbeTypeHTTP,
		Target:        netcupv1.ProbeTarget{Address: server.URL, Port: int32(port)},
		NetworkPolicy: netcupv1.ProbeNetworkPolicy{AllowPrivateNetworks: true},
	})
	if !result.Success {
		t.Fatalf("explicit URL probe failed: %s", result.Reason)
	}
}

func TestTLSProbeRejectsInvalidCABundle(t *testing.T) {
	result := ExecuteWithCredentialAndCABundle(context.Background(), netcupv1.FailoverProbeSpec{
		Phase:         netcupv1.ProbePhasePreRoute,
		Type:          netcupv1.ProbeTypeTLS,
		Target:        netcupv1.ProbeTarget{Address: "127.0.0.1", Port: 443},
		NetworkPolicy: netcupv1.ProbeNetworkPolicy{AllowPrivateNetworks: true},
	}, "", []byte("not a certificate"))
	if result.Success || result.Reason != "invalid CA bundle" {
		t.Fatalf("result = %#v, want invalid CA bundle", result)
	}
}

func TestHTTPProbeRejectsUnexpectedStatusAndBody(t *testing.T) {
	server := newLoopbackServer(t, "127.0.0.1", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/status":
			w.WriteHeader(http.StatusServiceUnavailable)
		case "/body":
			_, _ = io.WriteString(w, "service is starting")
		}
	}))
	defer server.Close()
	target := targetFromServerURL(t, server.URL)

	tests := []struct {
		name   string
		path   string
		match  string
		reason string
	}{
		{name: "status", path: "/status", reason: "unexpected HTTP status"},
		{name: "body", path: "/body", match: "service is ready", reason: "response body did not match"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			target.Path = tt.path
			result := Execute(context.Background(), netcupv1.FailoverProbeSpec{
				Phase:         netcupv1.ProbePhasePostRoute,
				Type:          netcupv1.ProbeTypeHTTP,
				Target:        target,
				BodyMatch:     tt.match,
				NetworkPolicy: netcupv1.ProbeNetworkPolicy{AllowPrivateNetworks: true},
			})
			if result.Success || result.Reason != tt.reason {
				t.Fatalf("result = %#v, want failure reason %q", result, tt.reason)
			}
		})
	}
}

func TestHTTPProbeInjectsCredentialsWithoutLeakingThem(t *testing.T) {
	const secret = "probe-secret-value"
	for _, tt := range []struct {
		name             string
		credentialHeader string
		wantHeader       string
	}{
		{name: "default authorization", wantHeader: "Authorization"},
		{name: "custom header", credentialHeader: "X-API-Key", wantHeader: "X-API-Key"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var gotCredential string
			server := newLoopbackServer(t, "127.0.0.1", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotCredential = r.Header.Get(tt.wantHeader)
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = io.WriteString(w, "backend rejected "+secret)
			}))
			defer server.Close()

			result := ExecuteWithCredential(context.Background(), netcupv1.FailoverProbeSpec{
				Phase:            netcupv1.ProbePhasePostRoute,
				Type:             netcupv1.ProbeTypeHTTP,
				Target:           targetFromServerURL(t, server.URL),
				CredentialHeader: tt.credentialHeader,
				NetworkPolicy:    netcupv1.ProbeNetworkPolicy{AllowPrivateNetworks: true},
			}, secret)
			if result.Success {
				t.Fatal("credential-protected HTTP failure was reported as success")
			}
			if gotCredential != secret {
				t.Fatalf("credential header = %q, want injected secret", gotCredential)
			}
			if strings.Contains(result.Reason, secret) {
				t.Fatalf("probe reason leaked credential: %q", result.Reason)
			}
		})
	}
}

func TestHTTPProbeRejectsOversizedResponseBody(t *testing.T) {
	server := newLoopbackServer(t, "127.0.0.1", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, strings.Repeat("x", maxResponseBody+1))
	}))
	defer server.Close()

	result := Execute(context.Background(), netcupv1.FailoverProbeSpec{
		Phase:         netcupv1.ProbePhasePostRoute,
		Type:          netcupv1.ProbeTypeHTTP,
		Target:        targetFromServerURL(t, server.URL),
		NetworkPolicy: netcupv1.ProbeNetworkPolicy{AllowPrivateNetworks: true},
	})
	if result.Success || result.Reason != "response body exceeded limit" {
		t.Fatalf("result = %#v, want bounded-body failure", result)
	}
}

func TestHTTPProbeBlocksRedirectToUnallowedPrivateNetwork(t *testing.T) {
	var redirectHits int
	privateServer := newLoopbackServer(t, "127.0.0.2", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		redirectHits++
		w.WriteHeader(http.StatusOK)
	}))
	entryServer := newLoopbackServer(t, "127.0.0.1", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, privateServer.URL, http.StatusFound)
	}))
	defer entryServer.Close()

	target := targetFromServerURL(t, entryServer.URL)
	result := Execute(context.Background(), netcupv1.FailoverProbeSpec{
		Phase:           netcupv1.ProbePhasePostRoute,
		Type:            netcupv1.ProbeTypeHTTP,
		Target:          target,
		FollowRedirects: true,
		NetworkPolicy: netcupv1.ProbeNetworkPolicy{
			AllowedCIDRs: []string{"127.0.0.1/32"},
		},
	})
	if result.Success {
		t.Fatal("redirect to an unallowed private network succeeded")
	}
	if redirectHits != 0 {
		t.Fatal("redirect target received a request before policy validation")
	}
}

func targetFromServerURL(t *testing.T, rawURL string) netcupv1.ProbeTarget {
	t.Helper()
	parsed, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("parse test server URL: %v", err)
	}
	host, portText, err := net.SplitHostPort(parsed.Host)
	if err != nil {
		t.Fatalf("split test server address: %v", err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatalf("parse test server port: %v", err)
	}
	return netcupv1.ProbeTarget{Address: host, Port: int32(port)}
}

func newLoopbackServer(t *testing.T, address string, handler http.Handler) *httptest.Server {
	t.Helper()
	listener, err := net.Listen("tcp", net.JoinHostPort(address, "0"))
	if err != nil {
		if os.IsPermission(err) || strings.Contains(err.Error(), "operation not permitted") {
			t.Skipf("network sockets unavailable in this environment: %v", err)
		}
		t.Fatalf("listen on %s: %v", address, err)
	}
	server := httptest.NewUnstartedServer(handler)
	server.Listener = listener
	server.Start()
	t.Cleanup(server.Close)
	return server
}
