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

func TestValidateTargetPolicyPrecedenceAndCIDRs(t *testing.T) {
	if err := validateTarget(context.Background(), netcupv1.ProbeTarget{Address: "127.0.0.1"}, netcupv1.ProbeNetworkPolicy{
		AllowedCIDRs: []string{"127.0.0.1/32"},
	}); err != nil {
		t.Fatalf("explicitly allowed sensitive address rejected: %v", err)
	}

	if err := validateTarget(context.Background(), netcupv1.ProbeTarget{Address: "127.0.0.1"}, netcupv1.ProbeNetworkPolicy{
		AllowPrivateNetworks: true,
		DeniedCIDRs:          []string{"127.0.0.0/8"},
	}); err == nil {
		t.Fatal("denied CIDR did not override private-network exception")
	}

	if err := validateTarget(context.Background(), netcupv1.ProbeTarget{Address: "127.0.0.1"}, netcupv1.ProbeNetworkPolicy{
		AllowedCIDRs: []string{"not-a-cidr"},
	}); err == nil {
		t.Fatal("invalid allowed CIDR was accepted")
	}
	if err := validateTarget(context.Background(), netcupv1.ProbeTarget{Address: "127.0.0.1"}, netcupv1.ProbeNetworkPolicy{
		DeniedCIDRs: []string{"not-a-cidr"},
	}); err == nil {
		t.Fatal("invalid denied CIDR was accepted")
	}
}

func TestValidateTargetRejectsSensitiveAddressFamilies(t *testing.T) {
	for _, address := range []string{"10.0.0.1", "172.16.0.1", "192.168.1.1", "169.254.1.1", "100.64.0.1", "::1", "fc00::1", "fe80::1"} {
		if err := validateTarget(context.Background(), netcupv1.ProbeTarget{Address: address}, netcupv1.ProbeNetworkPolicy{}); err == nil {
			t.Errorf("sensitive address %s was accepted", address)
		}
	}
}
