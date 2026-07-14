package probe

import (
	"context"
	"testing"

	netcupv1 "github.com/thorion3006/foip-operator/api/v1"
)

func TestValidateTargetBlocksSensitiveNetworks(t *testing.T) {
	err := validateTarget(context.Background(), netcupv1.ProbeTarget{Address: "127.0.0.1"}, netcupv1.ProbeNetworkPolicy{})
	if err == nil {
		t.Fatal("loopback target was accepted")
	}
	if err := validateTarget(context.Background(), netcupv1.ProbeTarget{Address: "127.0.0.1"}, netcupv1.ProbeNetworkPolicy{AllowPrivateNetworks: true}); err != nil {
		t.Fatalf("explicit private-network exception rejected: %v", err)
	}
}
