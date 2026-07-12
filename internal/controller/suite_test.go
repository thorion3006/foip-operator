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

package controller

import (
	"fmt"
	"os"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	netcupv1 "github.com/thorion3006/foip-operator/api/v1"
)

var k8sClient client.Client

func TestMain(m *testing.M) {
	logf.SetLogger(zap.New(zap.WriteTo(os.Stderr), zap.UseDevMode(true)))

	if err := netcupv1.AddToScheme(scheme.Scheme); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "add scheme: %v\n", err)
		os.Exit(1)
	}
	if err := corev1.AddToScheme(scheme.Scheme); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "add core scheme: %v\n", err)
		os.Exit(1)
	}

	k8sClient = fake.NewClientBuilder().
		WithScheme(scheme.Scheme).
		WithStatusSubresource(&netcupv1.FailoverIp{}).
		Build()

	os.Exit(m.Run())
}
