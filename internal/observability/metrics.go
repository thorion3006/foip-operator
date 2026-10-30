/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package observability

import (
	"slices"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	crmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"
)

var registerMetricsOnce sync.Once

// Metric names and label keys are constants so a typo cannot silently split a
// series or invent a new label at a call site.
const (
	metricNamespace = "foip"

	subsystemController = "controller"
	subsystemProvider   = "provider"
	subsystemInterface  = "interface"
	subsystemFailover   = "failover"

	labelController = "controller"
	labelProvider   = "provider"
	labelOperation  = "operation"
	labelResult     = "result"
	labelPhase      = "phase"
)

var (
	reconcileTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: metricNamespace,
		Subsystem: subsystemController,
		Name:      "reconcile_total",
		Help:      "Total number of controller reconciliations by outcome",
	}, []string{labelController, labelResult})
	reconcileDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: metricNamespace,
		Subsystem: subsystemController,
		Name:      "reconcile_duration_seconds",
		Help:      "Reconcile duration in seconds by controller and result",
		Buckets:   prometheus.ExponentialBuckets(0.005, 2, 14),
	}, []string{labelController, labelResult})
	providerTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: metricNamespace,
		Subsystem: subsystemProvider,
		Name:      "request_total",
		Help:      "Total number of provider requests by outcome",
	}, []string{labelProvider, labelOperation, labelResult})
	providerDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: metricNamespace,
		Subsystem: subsystemProvider,
		Name:      "request_duration_seconds",
		Help:      "Provider request duration in seconds",
		Buckets:   prometheus.ExponentialBuckets(0.005, 2, 14),
	}, []string{labelProvider, labelOperation})
	interfaceTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: metricNamespace,
		Subsystem: subsystemInterface,
		Name:      "operation_total",
		Help:      "Total number of local interface operations by outcome",
	}, []string{labelController, labelOperation, labelResult})
	interfaceDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: metricNamespace,
		Subsystem: subsystemInterface,
		Name:      "operation_duration_seconds",
		Help:      "Local interface operation duration in seconds",
		Buckets:   prometheus.ExponentialBuckets(0.005, 2, 14),
	}, []string{labelController, labelOperation})
	handoffDuration = prometheus.NewHistogram(prometheus.HistogramOpts{
		Namespace: metricNamespace,
		Subsystem: subsystemFailover,
		Name:      "handoff_duration_seconds",
		Help:      "Time spent completing a make-before-break failover handoff",
		Buckets:   prometheus.ExponentialBuckets(0.01, 2, 16),
	})
	phaseDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: metricNamespace,
		Subsystem: subsystemFailover,
		Name:      "phase_duration_seconds",
		Help:      "Time spent in a persisted failover phase",
		Buckets:   prometheus.ExponentialBuckets(0.01, 2, 16),
	}, []string{labelPhase})
	phaseTransitions = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: metricNamespace,
		Subsystem: subsystemFailover,
		Name:      "phase_transition_total",
		Help:      "Persisted failover phase transitions",
	}, []string{"from", "to"})
	cooldownBlocks = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: metricNamespace,
		Subsystem: subsystemProvider,
		Name:      "cooldown_block_total",
		Help:      "Provider mutations blocked by cooldown",
	})
	recoveryActions = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: metricNamespace,
		Subsystem: subsystemFailover,
		Name:      "recovery_action_total",
		Help:      "Post-route recovery actions by policy",
	}, []string{"policy"})
	ownerCount = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: metricNamespace,
		Subsystem: subsystemFailover,
		Name:      "local_owner_count",
		Help:      "Observed local failover IP owner count",
	}, []string{labelPhase})
	phaseState = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: metricNamespace,
		Subsystem: subsystemFailover,
		Name:      "phase",
		Help:      "Current persisted failover phase (one for the current phase, zero otherwise)",
	}, []string{labelPhase})
)

func init() {
	registerMetricsOnce.Do(func() {
		crmetrics.Registry.MustRegister(
			reconcileTotal,
			reconcileDuration,
			providerTotal,
			providerDuration,
			interfaceTotal,
			interfaceDuration,
			handoffDuration,
			phaseDuration,
			phaseTransitions,
			cooldownBlocks,
			recoveryActions,
			ownerCount,
			phaseState,
		)
	})
}

// ObserveReconcile records the outcome and duration of a controller reconcile.
func ObserveReconcile(controller, result string, duration time.Duration) {
	controller, result = boundedLabel(controller, "failoverip", "nodeinterface"), boundedLabel(result, "success", "error", "requeue_after")
	reconcileTotal.WithLabelValues(controller, result).Inc()
	reconcileDuration.WithLabelValues(controller, result).Observe(duration.Seconds())
}

// ObserveProviderCall records provider call timing and result.
func ObserveProviderCall(provider, operation string, duration time.Duration, err error) {
	result := "success"
	if err != nil {
		result = "error"
	}
	provider = boundedLabel(provider, "netcup", "kubernetes")
	operation = boundedLabel(operation, "find_failover_ip", "route_failover_ip", "verify_failover_ip", "get_secret")
	providerTotal.WithLabelValues(provider, operation, result).Inc()
	providerDuration.WithLabelValues(provider, operation).Observe(duration.Seconds())
}

// ObserveInterfaceOperation records local interface mutation timing and result.
func ObserveInterfaceOperation(controller, operation string, duration time.Duration, err error) {
	result := "success"
	if err != nil {
		result = "error"
	}
	controller = boundedLabel(controller, "nodeinterface")
	operation = boundedLabel(operation, "assign", "remove")
	interfaceTotal.WithLabelValues(controller, operation, result).Inc()
	interfaceDuration.WithLabelValues(controller, operation).Observe(duration.Seconds())
}

// ObserveHandoffDuration records the end-to-end duration of a successful handoff.
func ObserveHandoffDuration(duration time.Duration) {
	handoffDuration.Observe(duration.Seconds())
}

func ObservePhase(phase string, duration time.Duration) {
	if phase != "" {
		phaseDuration.WithLabelValues(boundedPhase(phase)).Observe(duration.Seconds())
	}
}

func ObservePhaseTransition(from, to string) {
	if from != "" && to != "" {
		phaseTransitions.WithLabelValues(boundedPhase(from), boundedPhase(to)).Inc()
	}
}

func ObserveCooldownBlock() { cooldownBlocks.Inc() }

func ObserveRecoveryAction(policy string) {
	if policy != "" {
		recoveryActions.WithLabelValues(boundedLabel(policy, "HoldDualOwnership", "RollbackProvider", "CommitDegraded", "ManualIntervention")).Inc()
	}
}

func ObserveOwnerCount(phase string, count int) {
	if phase != "" {
		ownerCount.WithLabelValues(boundedPhase(phase)).Set(float64(count))
	}
}

// ObservePhaseState exposes the persisted state without a resource label.
func ObservePhaseState(phase string) {
	phase = boundedPhase(phase)
	for _, candidate := range []string{"Idle", "Selecting", "Stabilizing", "PreparingTarget", "TargetPrepared", "RoutingProvider", "VerifyingProvider", "VerifyingTraffic", "Committing", "CleaningStaleOwners", "Succeeded", "Degraded", "Blocked", "other"} {
		phaseState.WithLabelValues(candidate).Set(0)
	}
	phaseState.WithLabelValues(phase).Set(1)
}

func boundedPhase(value string) string {
	return boundedLabel(value, "Idle", "Selecting", "Stabilizing", "PreparingTarget", "TargetPrepared", "RoutingProvider", "VerifyingProvider", "VerifyingTraffic", "Committing", "CleaningStaleOwners", "Succeeded", "Degraded", "Blocked")
}

func boundedLabel(value string, allowed ...string) string {
	if slices.Contains(allowed, value) {
		return value
	}
	return "other"
}
