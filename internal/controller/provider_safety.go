package controller

import (
	"fmt"
	"math/rand/v2"
	"time"

	netcupv1 "github.com/thorion3006/foip-operator/api/v1"
)

const (
	defaultProviderCooldown = time.Minute
	defaultRetryBase        = 2 * time.Second
	defaultRetryMax         = time.Minute
)

// providerMutationGate returns the earliest time a route mutation may run.
// The timestamps are persisted in status, so a restart cannot reset safety.
func providerMutationGate(status netcupv1.FailoverIpStatus, cooldown time.Duration, now time.Time) (time.Time, bool) {
	if status.LastAttemptedProviderMutationAt == nil {
		return time.Time{}, true
	}
	next := status.LastAttemptedProviderMutationAt.Add(cooldown)
	return next, !now.Before(next)
}

func providerCooldown(spec netcupv1.FailoverIpSpec) time.Duration {
	if spec.ProviderCooldownSeconds <= 0 {
		return defaultProviderCooldown
	}
	return time.Duration(spec.ProviderCooldownSeconds) * time.Second
}

func retryDelay(spec netcupv1.FailoverIpSpec, retryCount int32) time.Duration {
	base := defaultRetryBase
	max := defaultRetryMax
	if spec.RetryBaseSeconds > 0 {
		base = time.Duration(spec.RetryBaseSeconds) * time.Second
	}
	if spec.RetryMaxSeconds > 0 {
		max = time.Duration(spec.RetryMaxSeconds) * time.Second
	}
	if max < base {
		max = base
	}
	delay := base
	for i := int32(0); i < retryCount && delay < max; i++ {
		delay *= 2
		if delay > max {
			delay = max
		}
	}
	// Full jitter avoids synchronized controller replicas retrying together.
	jitter := time.Duration(rand.Int64N(int64(delay) + 1))
	if jitter < time.Second {
		return time.Second
	}
	return jitter
}

func validateProviderFence(status netcupv1.FailoverIpStatus, observedOwner, targetOwner string) error {
	if status.ProviderObservedOwner != "" && status.ProviderObservedOwner != observedOwner {
		return fmt.Errorf("provider owner changed out of band from %s to %s", status.ProviderObservedOwner, observedOwner)
	}
	if observedOwner == targetOwner {
		return nil
	}
	return nil
}

// validateProviderRecoveryFence permits the one expected owner change made by
// a persisted rollback while still rejecting an unrelated out-of-band owner.
func validateProviderRecoveryFence(status netcupv1.FailoverIpStatus, observedOwner, expectedOwner string) error {
	if status.ProviderObservedOwner != "" && status.ProviderObservedOwner != observedOwner && observedOwner != expectedOwner {
		return fmt.Errorf("provider owner changed out of band from %s to %s", status.ProviderObservedOwner, observedOwner)
	}
	return nil
}
