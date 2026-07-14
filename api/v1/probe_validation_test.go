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
	}
	for i, spec := range tests {
		if err := ValidateProbeSpec(spec); err == nil {
			t.Errorf("case %d was accepted", i)
		}
	}
}
