package v1

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
)

func TestValidateProbeSpec(t *testing.T) {
	valid := FailoverProbeSpec{Phase: ProbePhasePreRoute, Type: ProbeTypeTCP, Target: ProbeTarget{Address: "${targetNodeIP}", Port: 443}}
	if err := ValidateProbeSpec(valid); err != nil {
		t.Fatalf("valid probe rejected: %v", err)
	}
	tests := []FailoverProbeSpec{
		{Phase: ProbePhase("Unknown"), Type: ProbeTypeTCP, Target: ProbeTarget{Address: "node", Port: 443}},
		{Phase: ProbePhasePreRoute, Type: ProbeTypeTCP, Target: ProbeTarget{Address: "node", Port: 0}},
		{Phase: ProbePhasePreRoute, Type: ProbeTypeTCP, Target: ProbeTarget{Address: "node", Port: 443}, InsecureSkipVerify: true},
		{Phase: ProbePhasePreRoute, Type: ProbeTypeKubernetes},
		{Phase: ProbePhasePreRoute, Type: ProbeTypeTCP, Composition: ProbeCompositionQuorum, Quorum: 0, Target: ProbeTarget{Address: "node", Port: 443}},
		{Phase: ProbePhasePreRoute, Type: ProbeTypeHTTP, Target: ProbeTarget{Address: "node", Port: 80}, ExpectedStatusMin: 500, ExpectedStatusMax: 200},
		{Phase: ProbePhasePreRoute, Type: ProbeTypeHTTP, Target: ProbeTarget{Address: "node", Port: 80}, ExpectedStatusMin: 600},
		{Phase: ProbePhasePreRoute, Type: ProbeTypeHTTP, Target: ProbeTarget{Address: "node", Port: 80}, ExpectedStatusMax: 600},
		{Phase: ProbePhasePreRoute, Type: ProbeTypeHTTP, Target: ProbeTarget{Address: "node", Port: 80}, CABundleSecretRef: &corev1.SecretKeySelector{Key: "ca"}},
	}
	for i, spec := range tests {
		if err := ValidateProbeSpec(spec); err == nil {
			t.Errorf("case %d was accepted", i)
		}
	}
}

func TestValidateProbeSpecAcceptsHTTPMatchingConfiguration(t *testing.T) {
	spec := FailoverProbeSpec{
		Phase:             ProbePhasePostRoute,
		Type:              ProbeTypeHTTPS,
		Target:            ProbeTarget{Address: "service.example", Port: 443, Path: "/ready"},
		Method:            "HEAD",
		ExpectedStatusMin: 200,
		ExpectedStatusMax: 204,
		BodyMatch:         "ignored for HEAD",
		Headers:           make([]ProbeHeader, 32),
	}
	if err := ValidateProbeSpec(spec); err != nil {
		t.Fatalf("valid HTTP matching configuration rejected: %v", err)
	}
}

func TestValidateProbeSpecRejectsInvalidCombinations(t *testing.T) {
	base := FailoverProbeSpec{Phase: ProbePhasePreRoute, Type: ProbeTypeHTTP, Target: ProbeTarget{Address: "example.com", Port: 80}}
	tests := []struct {
		name string
		spec FailoverProbeSpec
	}{
		{name: "negative timeout", spec: func() FailoverProbeSpec { s := base; s.TimeoutSeconds = -1; return s }()},
		{name: "any with quorum", spec: func() FailoverProbeSpec { s := base; s.Composition = ProbeCompositionAny; s.Quorum = 1; return s }()},
		{name: "credential on TCP", spec: func() FailoverProbeSpec {
			s := base
			s.Type = ProbeTypeTCP
			s.CredentialSecretRef = &corev1.SecretKeySelector{}
			return s
		}()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := ValidateProbeSpec(tt.spec); err == nil {
				t.Fatal("invalid probe was accepted")
			}
		})
	}
}

func TestValidateFailoverIpSpecRejectsQuorumWithoutProbes(t *testing.T) {
	if err := ValidateFailoverIpSpec(FailoverIpSpec{ProbeComposition: ProbeCompositionAny, ProbeQuorum: 1}); err == nil {
		t.Fatal("non-quorum threshold was accepted")
	}
}

func FuzzValidateProbeSpecNeverPanics(f *testing.F) {
	f.Add("PreRoute", "TCP", "127.0.0.1", int32(443))
	f.Add("Unknown", "Unknown", "${targetNodeIP}", int32(-1))
	f.Fuzz(func(t *testing.T, phase, probeType, address string, port int32) {
		_ = ValidateProbeSpec(FailoverProbeSpec{
			Phase:  ProbePhase(phase),
			Type:   ProbeType(probeType),
			Target: ProbeTarget{Address: address, Port: port},
		})
	})
}
