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
	"context"
	"strings"
	"testing"
	"time"

	"github.com/onsi/gomega"
	dto "github.com/prometheus/client_model/go"
	"go.opentelemetry.io/otel/trace"
	crmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"
)

func TestTraceFields(t *testing.T) {
	t.Helper()
	g := gomega.NewWithT(t)

	sc := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    trace.TraceID([16]byte{0x1, 0x2, 0x3, 0x4, 0x5, 0x6, 0x7, 0x8, 0x9, 0x10, 0x11, 0x12, 0x13, 0x14, 0x15, 0x16}),
		SpanID:     trace.SpanID([8]byte{0x2, 0x3, 0x4, 0x5, 0x6, 0x7, 0x8, 0x9}),
		TraceFlags: trace.FlagsSampled,
	})
	fields := TraceFields(trace.ContextWithSpanContext(context.Background(), sc))
	g.Expect(fields).To(gomega.Equal([]any{"trace_id", sc.TraceID().String(), "span_id", sc.SpanID().String()}))
}

func TestObserveReconcileRegistersMetric(t *testing.T) {
	t.Helper()
	g := gomega.NewWithT(t)

	ObserveReconcile("failoverip", "success", 150*time.Millisecond)

	mfs, err := crmetrics.Registry.Gather()
	g.Expect(err).NotTo(gomega.HaveOccurred())
	var metric *dto.MetricFamily
	for _, mf := range mfs {
		if mf.GetName() == "foip_controller_reconcile_total" {
			metric = mf
			break
		}
	}
	g.Expect(metric).NotTo(gomega.BeNil())
	g.Expect(metric.GetMetric()).NotTo(gomega.BeEmpty())
	counter := metric.GetMetric()[0].GetCounter()
	g.Expect(counter.GetValue()).To(gomega.BeNumerically(">=", 1))
}

func TestObserveSafetyMetricsUseBoundedLabels(t *testing.T) {
	ObservePhaseTransition("TargetPrepared", "RoutingProvider")
	ObserveCooldownBlock()
	ObserveRecoveryAction("HoldDualOwnership")
	ObserveOwnerCount("CleaningStaleOwners", 2)
	ObservePhase("VerifyingTraffic", time.Second)

	mfs, err := crmetrics.Registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, mf := range mfs {
		if mf.GetName() == "foip_failover_phase_transition_total" {
			for _, metric := range mf.GetMetric() {
				for _, label := range metric.GetLabel() {
					if label.GetName() == "resource" || label.GetName() == "ip" || label.GetName() == "url" {
						t.Fatalf("unsafe high-cardinality label %q present", label.GetName())
					}
				}
			}
		}
	}
}

func TestEventDeduperSuppressesRepeatedEvents(t *testing.T) {
	d := NewEventDeduper(time.Minute)
	now := time.Unix(100, 0)
	if !d.Allow("same-failure", now) {
		t.Fatal("first event was suppressed")
	}
	if d.Allow("same-failure", now.Add(time.Second)) {
		t.Fatal("repeated event was not suppressed")
	}
	if !d.Allow("same-failure", now.Add(time.Minute)) {
		t.Fatal("event was not allowed after the deduplication window")
	}
}

func TestRedactTextRemovesSensitiveTelemetryValues(t *testing.T) {
	input := "request https://example.test/login from 192.0.2.10 authorization=Bearer-secret refreshToken=secret-value"
	got := RedactText(input)
	for _, secret := range []string{"https://example.test/login", "192.0.2.10", "Bearer-secret", "secret-value"} {
		if strings.Contains(got, secret) {
			t.Fatalf("telemetry retained %q: %q", secret, got)
		}
	}
}

func TestMetricLabelsFallbackToBoundedValues(t *testing.T) {
	ObserveReconcile("resource-name", "retry-123", time.Second)
	ObserveProviderCall("provider-123", "operation-123", time.Second, nil)
	ObservePhase("phase-123", time.Second)
	ObserveRecoveryAction("policy-123")
	// The calls above must not create labels containing their arbitrary values.
	mfs, err := crmetrics.Registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, mf := range mfs {
		for _, metric := range mf.GetMetric() {
			for _, label := range metric.GetLabel() {
				if strings.Contains(label.GetValue(), "123") {
					t.Fatalf("unbounded label value %q in %s", label.GetValue(), mf.GetName())
				}
			}
		}
	}
}

func TestObservePhaseStateExposesOneCurrentPhase(t *testing.T) {
	ObservePhaseState("Blocked")
	mfs, err := crmetrics.Registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, mf := range mfs {
		if mf.GetName() != "foip_failover_phase" {
			continue
		}
		for _, metric := range mf.GetMetric() {
			for _, label := range metric.GetLabel() {
				if label.GetName() == "phase" && label.GetValue() == "Blocked" && metric.GetGauge().GetValue() != 1 {
					t.Fatalf("blocked phase gauge = %v, want 1", metric.GetGauge().GetValue())
				}
			}
		}
	}
}
