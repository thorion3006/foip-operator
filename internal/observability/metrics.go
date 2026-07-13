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
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	crmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"
)

var registerMetricsOnce sync.Once

var (
	reconcileTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "foip",
		Subsystem: "controller",
		Name:      "reconcile_total",
		Help:      "Total number of controller reconciliations by outcome",
	}, []string{"controller", "result"})
	reconcileDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: "foip",
		Subsystem: "controller",
		Name:      "reconcile_duration_seconds",
		Help:      "Reconcile duration in seconds by controller and result",
		Buckets:   prometheus.ExponentialBuckets(0.005, 2, 14),
	}, []string{"controller", "result"})
	providerTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "foip",
		Subsystem: "provider",
		Name:      "request_total",
		Help:      "Total number of provider requests by outcome",
	}, []string{"provider", "operation", "result"})
	providerDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: "foip",
		Subsystem: "provider",
		Name:      "request_duration_seconds",
		Help:      "Provider request duration in seconds",
		Buckets:   prometheus.ExponentialBuckets(0.005, 2, 14),
	}, []string{"provider", "operation"})
	interfaceTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "foip",
		Subsystem: "interface",
		Name:      "operation_total",
		Help:      "Total number of local interface operations by outcome",
	}, []string{"controller", "operation", "result"})
	interfaceDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: "foip",
		Subsystem: "interface",
		Name:      "operation_duration_seconds",
		Help:      "Local interface operation duration in seconds",
		Buckets:   prometheus.ExponentialBuckets(0.005, 2, 14),
	}, []string{"controller", "operation"})
	handoffDuration = prometheus.NewHistogram(prometheus.HistogramOpts{
		Namespace: "foip",
		Subsystem: "failover",
		Name:      "handoff_duration_seconds",
		Help:      "Time spent completing a make-before-break failover handoff",
		Buckets:   prometheus.ExponentialBuckets(0.01, 2, 16),
	})
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
		)
	})
}

// ObserveReconcile records the outcome and duration of a controller reconcile.
func ObserveReconcile(controller, result string, duration time.Duration) {
	reconcileTotal.WithLabelValues(controller, result).Inc()
	reconcileDuration.WithLabelValues(controller, result).Observe(duration.Seconds())
}

// ObserveProviderCall records provider call timing and result.
func ObserveProviderCall(provider, operation string, duration time.Duration, err error) {
	result := "success"
	if err != nil {
		result = "error"
	}
	providerTotal.WithLabelValues(provider, operation, result).Inc()
	providerDuration.WithLabelValues(provider, operation).Observe(duration.Seconds())
}

// ObserveInterfaceOperation records local interface mutation timing and result.
func ObserveInterfaceOperation(controller, operation string, duration time.Duration, err error) {
	result := "success"
	if err != nil {
		result = "error"
	}
	interfaceTotal.WithLabelValues(controller, operation, result).Inc()
	interfaceDuration.WithLabelValues(controller, operation).Observe(duration.Seconds())
}

// ObserveHandoffDuration records the end-to-end duration of a successful handoff.
func ObserveHandoffDuration(duration time.Duration) {
	handoffDuration.Observe(duration.Seconds())
}
