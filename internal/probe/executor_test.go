package probe

import (
	"context"
	"fmt"
	"net"
	"os"
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
	defer listener.Close()
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr == nil {
			_ = conn.Close()
		}
	}()
	host, port, _ := net.SplitHostPort(listener.Addr().String())
	var p int
	_, _ = fmt.Sscan(port, &p)
	result := Execute(context.Background(), netcupv1.FailoverProbeSpec{Phase: netcupv1.ProbePhasePreRoute, Type: netcupv1.ProbeTypeTCP, Target: netcupv1.ProbeTarget{Address: host, Port: int32(p)}})
	if !result.Success {
		t.Fatalf("TCP probe failed: %s", result.Reason)
	}
}
