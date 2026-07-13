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
