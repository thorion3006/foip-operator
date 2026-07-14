package controller

import (
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	netcupv1 "github.com/thorion3006/foip-operator/api/v1"
)

func TestProviderMutationGatePersistsCooldown(t *testing.T) {
	attempt := metav1.NewTime(time.Unix(100, 0))
	status := netcupv1.FailoverIpStatus{LastAttemptedProviderMutationAt: &attempt}
	next, allowed := providerMutationGate(status, time.Minute, time.Unix(150, 0))
	if allowed || !next.Equal(time.Unix(160, 0)) {
		t.Fatalf("gate = (%s, %v), want (160s, false)", next, allowed)
	}
	_, allowed = providerMutationGate(status, time.Minute, time.Unix(160, 0))
	if !allowed {
		t.Fatal("mutation should be allowed at cooldown boundary")
	}
}

func TestRetryDelayIsBounded(t *testing.T) {
	spec := netcupv1.FailoverIpSpec{RetryBaseSeconds: 2, RetryMaxSeconds: 8}
	for i := int32(0); i < 8; i++ {
		delay := retryDelay(spec, i)
		if delay < 0 || delay > 8*time.Second {
			t.Fatalf("retry delay %s out of bounds", delay)
		}
	}
}

func TestValidateProviderFence(t *testing.T) {
	status := netcupv1.FailoverIpStatus{ProviderObservedOwner: "123"}
	if err := validateProviderFence(status, "456", "789"); err == nil {
		t.Fatal("expected out-of-band owner change to be rejected")
	}
}
