package probe

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	netcupv1 "github.com/thorion3006/foip-operator/api/v1"
)

const maxResponseBody = 1 << 20

// Result is intentionally bounded and contains no request headers or body.
type Result struct {
	Success bool
	Reason  string
}

// Execute runs one provider-neutral probe with a caller-owned context.
func Execute(ctx context.Context, spec netcupv1.FailoverProbeSpec) Result {
	return execute(ctx, spec, "")
}

// ExecuteWithCredential injects one Secret-derived value into the configured
// header. The value never appears in Result or any error string.
func ExecuteWithCredential(ctx context.Context, spec netcupv1.FailoverProbeSpec, value string) Result {
	return execute(ctx, spec, value)
}

func execute(ctx context.Context, spec netcupv1.FailoverProbeSpec, credential string) Result {
	if err := netcupv1.ValidateProbeSpec(spec); err != nil {
		return Result{Reason: err.Error()}
	}
	timeout := time.Duration(spec.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if spec.Type != netcupv1.ProbeTypeKubernetes {
		if err := validateTarget(ctx, spec.Target, spec.NetworkPolicy); err != nil {
			return Result{Reason: "target blocked by network policy"}
		}
	}

	switch spec.Type {
	case netcupv1.ProbeTypeTCP:
		return tcp(ctx, spec.Target, false, spec.InsecureSkipVerify)
	case netcupv1.ProbeTypeTLS:
		return tcp(ctx, spec.Target, true, spec.InsecureSkipVerify)
	case netcupv1.ProbeTypeHTTP, netcupv1.ProbeTypeHTTPS:
		return httpProbe(ctx, spec, credential)
	default:
		return Result{Reason: fmt.Sprintf("probe type %q has no network executor", spec.Type)}
	}
}

func tcp(ctx context.Context, target netcupv1.ProbeTarget, tlsMode, insecure bool) Result {
	address := net.JoinHostPort(target.Address, strconv.Itoa(int(target.Port)))
	dialer := &net.Dialer{}
	var conn net.Conn
	var err error
	if tlsMode {
		conn, err = tls.DialWithDialer(dialer, "tcp", address, &tls.Config{ServerName: target.SNI, MinVersion: tls.VersionTLS12, InsecureSkipVerify: insecure}) // #nosec G402 -- insecure mode is an explicit API opt-in
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", address)
	}
	if err != nil {
		return Result{Reason: "connection failed"}
	}
	_ = conn.Close()
	return Result{Success: true}
}

func httpProbe(ctx context.Context, spec netcupv1.FailoverProbeSpec, credential string) Result {
	scheme := "http"
	if spec.Type == netcupv1.ProbeTypeHTTPS {
		scheme = "https"
	}
	path := spec.Target.Path
	if path == "" {
		path = "/"
	}
	url := scheme + "://" + net.JoinHostPort(spec.Target.Address, strconv.Itoa(int(spec.Target.Port))) + path
	method := spec.Method
	if method == "" {
		method = http.MethodGet
	}
	req, err := http.NewRequestWithContext(ctx, method, url, nil)
	if err != nil {
		return Result{Reason: "invalid request"}
	}
	if spec.Target.Host != "" {
		req.Host = spec.Target.Host
	}
	if credential != "" {
		header := spec.CredentialHeader
		if header == "" {
			header = "Authorization"
		}
		req.Header.Set(header, credential)
	}
	for _, header := range spec.Headers {
		req.Header.Set(header.Name, header.Value)
	}
	transport := &http.Transport{TLSClientConfig: &tls.Config{ServerName: spec.Target.SNI, MinVersion: tls.VersionTLS12, InsecureSkipVerify: spec.InsecureSkipVerify}} // #nosec G402 -- insecure mode is an explicit API opt-in
	if !spec.FollowRedirects {
		transport.DisableKeepAlives = true
	}
	client := &http.Client{Transport: transport, CheckRedirect: func(next *http.Request, _ []*http.Request) error {
		if !spec.FollowRedirects {
			return http.ErrUseLastResponse
		}
		if err := validateTarget(next.Context(), netcupv1.ProbeTarget{Address: next.URL.Hostname()}, spec.NetworkPolicy); err != nil {
			return fmt.Errorf("redirect target blocked by network policy")
		}
		return nil
	}}
	resp, err := client.Do(req)
	if err != nil {
		return Result{Reason: "request failed"}
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxResponseBody+1))
	if len(body) > maxResponseBody {
		return Result{Reason: "response body exceeded limit"}
	}
	minStatus, maxStatus := spec.ExpectedStatusMin, spec.ExpectedStatusMax
	if minStatus == 0 {
		minStatus = 200
	}
	if maxStatus == 0 {
		maxStatus = 299
	}
	if int32(resp.StatusCode) < minStatus || int32(resp.StatusCode) > maxStatus {
		return Result{Reason: "unexpected HTTP status"}
	}
	if spec.BodyMatch != "" && !strings.Contains(string(body), spec.BodyMatch) {
		return Result{Reason: "response body did not match"}
	}
	return Result{Success: true}
}

// Aggregate applies deterministic composition semantics to probe outcomes.
func Aggregate(composition netcupv1.ProbeComposition, quorum int32, results []Result) Result {
	if len(results) == 0 {
		return Result{Reason: "no probe results"}
	}
	successes := 0
	for _, result := range results {
		if result.Success {
			successes++
		}
	}
	switch composition {
	case "", netcupv1.ProbeCompositionAll:
		if successes == len(results) {
			return Result{Success: true}
		}
		return Result{Reason: "not all probes succeeded"}
	case netcupv1.ProbeCompositionAny:
		if successes > 0 {
			return Result{Success: true}
		}
		return Result{Reason: "no probe succeeded"}
	case netcupv1.ProbeCompositionQuorum:
		if successes >= int(quorum) {
			return Result{Success: true}
		}
		return Result{Reason: fmt.Sprintf("quorum not reached: %d/%d", successes, quorum)}
	default:
		return Result{Reason: strings.TrimSpace("unsupported composition")}
	}
}
