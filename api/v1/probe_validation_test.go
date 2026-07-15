package v1

import "testing"

func TestValidateProbeSpec(t *testing.T) {
	valid := FailoverProbeSpec{Phase: ProbePhasePreRoute, Type: ProbeTypeTCP, Target: ProbeTarget{Address: "${targetNodeIP}", Port: 443}}
	if err := ValidateProbeSpec(valid); err != nil {
		t.Fatalf("valid probe rejected: %v", err)
	}
	tests := []FailoverProbeSpec{
		{Phase: ProbePhasePreRoute, Type: ProbeTypeTCP, Target: ProbeTarget{Address: "node", Port: 0}},
		{Phase: ProbePhasePreRoute, Type: ProbeTypeTCP, Target: ProbeTarget{Address: "node", Port: 443}, InsecureSkipVerify: true},
		{Phase: ProbePhasePreRoute, Type: ProbeTypeKubernetes},
		{Phase: ProbePhasePreRoute, Type: ProbeTypeTCP, Composition: ProbeCompositionQuorum, Quorum: 0, Target: ProbeTarget{Address: "node", Port: 443}},
		{Phase: ProbePhasePreRoute, Type: ProbeTypeHTTP, Target: ProbeTarget{Address: "node", Port: 80}, ExpectedStatusMin: 500, ExpectedStatusMax: 200},
		{Phase: ProbePhasePreRoute, Type: ProbeTypeHTTP, Target: ProbeTarget{Address: "node", Port: 80}, ExpectedStatusMin: 600},
		{Phase: ProbePhasePreRoute, Type: ProbeTypeHTTP, Target: ProbeTarget{Address: "node", Port: 80}, ExpectedStatusMax: 600},
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
