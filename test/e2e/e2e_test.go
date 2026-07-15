//go:build e2e

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

package e2e

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/thorion3006/foip-operator/test/utils"
)

// namespace where the project is deployed in
const namespace = "foip-operator-system"

// serviceAccountName created for the project
const serviceAccountName = "foip-operator-controller-manager"

// metricsServiceName is the name of the metrics service of the project
const metricsServiceName = "foip-operator-controller-manager-metrics-service"

// metricsRoleBindingName is the name of the RBAC that will be created to allow get the metrics data
const metricsRoleBindingName = "foip-operator-metrics-binding"

var _ = Describe("Manager", Ordered, func() {
	var controllerPodName string

	// Before running the tests, set up the environment by creating the namespace,
	// enforce the restricted security policy to the namespace, installing CRDs,
	// and deploying the controller.
	BeforeAll(func() {
		By("creating manager namespace")
		cmd := exec.Command("kubectl", "create", "ns", namespace)
		_, err := utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to create namespace")

		By("labeling the namespace to enforce the restricted security policy")
		cmd = exec.Command("kubectl", "label", "--overwrite", "ns", namespace,
			"pod-security.kubernetes.io/enforce=restricted")
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to label namespace with restricted policy")

		By("installing CRDs")
		cmd = exec.Command("make", "install")
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to install CRDs")

		By("deploying the controller-manager")
		cmd = exec.Command("make", "deploy", fmt.Sprintf("IMG=%s", managerImage))
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to deploy the controller-manager")

		By("deploying the deterministic fake provider")
		applyManifest(fmt.Sprintf(`apiVersion: apps/v1
kind: Deployment
metadata:
  name: fake-provider
  namespace: %s
  labels:
    app: fake-provider
spec:
  replicas: 1
  selector:
    matchLabels:
      app: fake-provider
  template:
    metadata:
      labels:
        app: fake-provider
    spec:
      securityContext:
        runAsNonRoot: true
        seccompProfile:
          type: RuntimeDefault
      containers:
      - name: fake-provider
        image: %s
        imagePullPolicy: IfNotPresent
        command: ["/fake-provider"]
        env:
        - name: FAKE_PROVIDER_ROUTE_DELAY_SECONDS
          value: "5"
        securityContext:
          allowPrivilegeEscalation: false
          capabilities:
            drop: ["ALL"]
`, namespace, managerImage))
		cmd = exec.Command("kubectl", "expose", "deployment", "fake-provider", "--port=8080", "--target-port=8080", "--namespace", namespace)
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to expose the fake provider")
		cmd = exec.Command("kubectl", "rollout", "status", "deployment/fake-provider", "--namespace", namespace, "--timeout=3m")
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Fake provider did not become ready")

		By("configuring the controller to use the fake provider")
		cmd = exec.Command("kubectl", "set", "env", "deployment/foip-operator-controller-manager", "-n", namespace,
			"NETCUP_API_BASE_URL=http://fake-provider:8080", "NETCUP_TOKEN_URL=http://fake-provider:8080/token")
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to configure the fake provider endpoint")
		cmd = exec.Command("kubectl", "rollout", "status", "deployment/foip-operator-controller-manager", "--namespace", namespace, "--timeout=3m")
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Controller did not restart with the fake provider endpoint")
	})

	// After all tests have been executed, clean up by undeploying the controller, uninstalling CRDs,
	// and deleting the namespace.
	AfterAll(func() {
		By("cleaning up the curl pod for metrics")
		cmd := exec.Command("kubectl", "delete", "pod", "curl-metrics", "-n", namespace)
		_, _ = utils.Run(cmd)

		By("cleaning up the metrics clusterrolebinding")
		cmd = exec.Command("kubectl", "delete", "clusterrolebinding", metricsRoleBindingName)
		_, _ = utils.Run(cmd)

		By("undeploying the controller-manager")
		cmd = exec.Command("make", "undeploy")
		_, _ = utils.Run(cmd)

		By("removing the fake provider")
		cmd = exec.Command("kubectl", "delete", "service,deployment", "fake-provider", "-n", namespace, "--ignore-not-found")
		_, _ = utils.Run(cmd)

		By("uninstalling CRDs")
		cmd = exec.Command("make", "uninstall")
		_, _ = utils.Run(cmd)

		By("removing manager namespace")
		cmd = exec.Command("kubectl", "delete", "ns", namespace)
		_, _ = utils.Run(cmd)
	})

	// After each test, check for failures and collect logs, events,
	// and pod descriptions for debugging.
	AfterEach(func() {
		specReport := CurrentSpecReport()
		if specReport.Failed() {
			By("Fetching controller manager pod logs")
			cmd := exec.Command("kubectl", "logs", controllerPodName, "-n", namespace)
			controllerLogs, err := utils.Run(cmd)
			if err == nil {
				_, _ = fmt.Fprintf(GinkgoWriter, "Controller logs:\n %s", controllerLogs)
			} else {
				_, _ = fmt.Fprintf(GinkgoWriter, "Failed to get Controller logs: %s", err)
			}

			By("Fetching Kubernetes events")
			cmd = exec.Command("kubectl", "get", "events", "-n", namespace, "--sort-by=.lastTimestamp")
			eventsOutput, err := utils.Run(cmd)
			if err == nil {
				_, _ = fmt.Fprintf(GinkgoWriter, "Kubernetes events:\n%s", eventsOutput)
			} else {
				_, _ = fmt.Fprintf(GinkgoWriter, "Failed to get Kubernetes events: %s", err)
			}

			By("Fetching curl-metrics logs")
			cmd = exec.Command("kubectl", "logs", "curl-metrics", "-n", namespace)
			metricsOutput, err := utils.Run(cmd)
			if err == nil {
				_, _ = fmt.Fprintf(GinkgoWriter, "Metrics logs:\n %s", metricsOutput)
			} else {
				_, _ = fmt.Fprintf(GinkgoWriter, "Failed to get curl-metrics logs: %s", err)
			}

			By("Fetching controller manager pod description")
			cmd = exec.Command("kubectl", "describe", "pod", controllerPodName, "-n", namespace)
			podDescription, err := utils.Run(cmd)
			if err == nil {
				fmt.Println("Pod description:\n", podDescription)
			} else {
				fmt.Println("Failed to describe controller pod")
			}
		}
	})

	SetDefaultEventuallyTimeout(2 * time.Minute)
	SetDefaultEventuallyPollingInterval(time.Second)

	Context("Manager", func() {
		It("should run successfully", func() {
			By("validating that the controller-manager pod is running as expected")
			verifyControllerUp := func(g Gomega) {
				// Get the name of the controller-manager pod
				cmd := exec.Command("kubectl", "get",
					"pods", "-l", "control-plane=controller-manager",
					"-o", "go-template={{ range .items }}"+
						"{{ if not .metadata.deletionTimestamp }}"+
						"{{ .metadata.name }}"+
						"{{ \"\\n\" }}{{ end }}{{ end }}",
					"-n", namespace,
				)

				podOutput, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred(), "Failed to retrieve controller-manager pod information")
				podNames := utils.GetNonEmptyLines(podOutput)
				g.Expect(podNames).To(HaveLen(1), "expected 1 controller pod running")
				controllerPodName = podNames[0]
				g.Expect(controllerPodName).To(ContainSubstring("controller-manager"))

				// Validate the pod's status
				cmd = exec.Command("kubectl", "get",
					"pods", controllerPodName, "-o", "jsonpath={.status.phase}",
					"-n", namespace,
				)
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("Running"), "Incorrect controller-manager pod status")
			}
			Eventually(verifyControllerUp).Should(Succeed())
		})

		It("should ensure the metrics endpoint is serving metrics", func() {
			By("creating a ClusterRoleBinding for the service account to allow access to metrics")
			cmd := exec.Command("kubectl", "delete", "clusterrolebinding", metricsRoleBindingName, "--ignore-not-found")
			_, _ = utils.Run(cmd)

			cmd = exec.Command("kubectl", "create", "clusterrolebinding", metricsRoleBindingName,
				"--clusterrole=foip-operator-metrics-reader",
				fmt.Sprintf("--serviceaccount=%s:%s", namespace, serviceAccountName),
			)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to create ClusterRoleBinding")

			By("validating that the metrics service is available")
			cmd = exec.Command("kubectl", "get", "service", metricsServiceName, "-n", namespace)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Metrics service should exist")

			By("getting the service account token")
			token, err := serviceAccountToken()
			Expect(err).NotTo(HaveOccurred())
			Expect(token).NotTo(BeEmpty())

			By("ensuring the controller pod is ready")
			verifyControllerPodReady := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "pod", controllerPodName, "-n", namespace,
					"-o", "jsonpath={.status.conditions[?(@.type=='Ready')].status}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("True"), "Controller pod not ready")
			}
			Eventually(verifyControllerPodReady, 3*time.Minute, time.Second).Should(Succeed())

			By("verifying that the controller manager is serving the metrics server")
			verifyMetricsServerStarted := func(g Gomega) {
				cmd := exec.Command("kubectl", "logs", controllerPodName, "-n", namespace)
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(ContainSubstring("Serving metrics server"),
					"Metrics server not yet started")
			}
			Eventually(verifyMetricsServerStarted, 3*time.Minute, time.Second).Should(Succeed())

			// +kubebuilder:scaffold:e2e-metrics-webhooks-readiness

			By("creating the curl-metrics pod to access the metrics endpoint")
			cmd = exec.Command("kubectl", "run", "curl-metrics", "--restart=Never",
				"--namespace", namespace,
				"--image=curlimages/curl:latest",
				"--overrides",
				fmt.Sprintf(`{
					"spec": {
						"containers": [{
							"name": "curl",
							"image": "curlimages/curl:latest",
							"command": ["/bin/sh", "-c"],
							"args": [
								"for i in $(seq 1 30); do curl -v -k -H 'Authorization: Bearer %s' https://%s.%s.svc.cluster.local:8443/metrics && exit 0 || sleep 2; done; exit 1"
							],
							"securityContext": {
								"readOnlyRootFilesystem": true,
								"allowPrivilegeEscalation": false,
								"capabilities": {
									"drop": ["ALL"]
								},
								"runAsNonRoot": true,
								"runAsUser": 1000,
								"seccompProfile": {
									"type": "RuntimeDefault"
								}
							}
						}],
						"serviceAccountName": "%s"
					}
				}`, token, metricsServiceName, namespace, serviceAccountName))
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to create curl-metrics pod")

			By("waiting for the curl-metrics pod to complete.")
			verifyCurlUp := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "pods", "curl-metrics",
					"-o", "jsonpath={.status.phase}",
					"-n", namespace)
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("Succeeded"), "curl pod in wrong status")
			}
			Eventually(verifyCurlUp, 5*time.Minute).Should(Succeed())

			By("getting the metrics by checking curl-metrics logs")
			verifyMetricsAvailable := func(g Gomega) {
				metricsOutput, err := getMetricsOutput()
				g.Expect(err).NotTo(HaveOccurred(), "Failed to retrieve logs from curl pod")
				g.Expect(metricsOutput).NotTo(BeEmpty())
				g.Expect(metricsOutput).To(MatchRegexp(`< HTTP/(1\.1|2) 200`))
			}
			Eventually(verifyMetricsAvailable, 2*time.Minute).Should(Succeed())
		})

		It("should complete a persisted handoff through the fake provider", func() {
			By("creating provider credentials")
			applyManifest(`apiVersion: v1
kind: Secret
metadata:
  name: e2e-netcup-credentials
  namespace: foip-operator-system
type: Opaque
stringData:
  userId: "42"
  refreshToken: fake-refresh-token
`)

			By("annotating a Kind node as a provider target")
			nodeOutput, err := utils.Run(exec.Command("kubectl", "get", "nodes", "-o", "jsonpath={.items[0].metadata.name}"))
			Expect(err).NotTo(HaveOccurred())
			nodeName := strings.TrimSpace(nodeOutput)
			Expect(nodeName).NotTo(BeEmpty())
			cmd := exec.Command("kubectl", "annotate", "node", nodeName,
				"foip.noshoes.xyz/server-id=12345", "foip.noshoes.xyz/mac-address=02:00:00:00:00:01", "--overwrite")
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("creating a target-prepared failover resource")
			applyManifest(fmt.Sprintf(`apiVersion: foip.noshoes.xyz/v1
kind: FailoverIp
metadata:
  name: e2e-handoff
  namespace: %s
spec:
  ip: 192.0.2.44
  secretName: e2e-netcup-credentials
`, namespace))
			cmd = exec.Command("kubectl", "patch", "failoverip", "e2e-handoff", "--subresource=status", "--type=merge",
				"-p", fmt.Sprintf(`{"status":{"transitionID":"e2e-transition","phase":"TargetPrepared","targetNode":%q,"localOwners":[%q]}}`, nodeName, nodeName), "-n", namespace)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("waiting for the handoff to converge")
			By("waiting until the provider mutation is durably in flight")
			Eventually(func(g Gomega) {
				phase, attempted := failoverStatusFields("e2e-handoff")
				g.Expect(phase).To(Equal("RoutingProvider"))
				g.Expect(attempted).NotTo(BeEmpty())
			}, time.Minute, time.Second).Should(Succeed())

			By("restarting the controller during provider routing")
			cmd = exec.Command("kubectl", "delete", "pod", "-l", "control-plane=controller-manager", "-n", namespace, "--wait")
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())
			cmd = exec.Command("kubectl", "rollout", "status", "deployment/foip-operator-controller-manager", "--namespace", namespace, "--timeout=3m")
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("waiting for the restarted handoff to converge")
			Eventually(func(g Gomega) {
				output, getErr := utils.Run(exec.Command("kubectl", "get", "failoverip", "e2e-handoff", "-n", namespace, "-o", "jsonpath={.status.phase}"))
				g.Expect(getErr).NotTo(HaveOccurred())
				g.Expect(strings.TrimSpace(output)).To(Equal("Succeeded"))
			}, 3*time.Minute, time.Second).Should(Succeed())

			By("verifying that the provider mutation was not duplicated")
			state := fakeProviderState()
			Expect(state.RouteCount).To(Equal(1))
			Expect(state.Owner).To(Equal(12345))

			By("simulating node-agent ownership loss and rejoin")
			applyManifest(fmt.Sprintf(`apiVersion: foip.noshoes.xyz/v1
kind: FailoverIp
metadata:
  name: e2e-ownership
  namespace: %s
spec:
  ip: 192.0.2.45
  secretName: e2e-netcup-credentials
`, namespace))
			cmd = exec.Command("kubectl", "patch", "failoverip", "e2e-ownership", "--subresource=status", "--type=merge",
				"-p", fmt.Sprintf(`{"status":{"transitionID":"ownership-transition","phase":"CleaningStaleOwners","sourceNode":%q,"targetNode":%q,"localOwners":[%q,"stale-node"]}}`, nodeName, nodeName, nodeName), "-n", namespace)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("holding the transition while the target agent is unavailable")
			cmd = exec.Command("kubectl", "patch", "failoverip", "e2e-ownership", "--subresource=status", "--type=merge",
				"-p", `{"status":{"localOwners":[]}}`, "-n", namespace)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())
			Eventually(func(g Gomega) {
				phase, _ := failoverStatusFields("e2e-ownership")
				g.Expect(phase).To(Equal("CleaningStaleOwners"))
			}, time.Minute, time.Second).Should(Succeed())

			By("accepting the target agent rejoin only after it reports sole ownership")
			cmd = exec.Command("kubectl", "patch", "failoverip", "e2e-ownership", "--subresource=status", "--type=merge",
				"-p", fmt.Sprintf(`{"status":{"localOwners":[%q]}}`, nodeName), "-n", namespace)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())
			Eventually(func(g Gomega) {
				output, getErr := utils.Run(exec.Command("kubectl", "get", "failoverip", "e2e-ownership", "-n", namespace, "-o", "jsonpath={.status.phase}"))
				g.Expect(getErr).NotTo(HaveOccurred())
				g.Expect(strings.TrimSpace(output)).To(Equal("Succeeded"))
			}, time.Minute, time.Second).Should(Succeed())
		})

		// +kubebuilder:scaffold:e2e-webhooks-checks

		// TODO: Customize the e2e test suite with scenarios specific to your project.
		// Consider applying sample/CR(s) and check their status and/or verifying
		// the reconciliation by using the metrics, i.e.:
		// metricsOutput, err := getMetricsOutput()
		// Expect(err).NotTo(HaveOccurred(), "Failed to retrieve logs from curl pod")
		// Expect(metricsOutput).To(ContainSubstring(
		//    fmt.Sprintf(`controller_runtime_reconcile_total{controller="%s",result="success"} 1`,
		//    strings.ToLower(<Kind>),
		// ))
	})
})

func applyManifest(manifest string) {
	file, err := os.CreateTemp("", "foip-e2e-*.yaml")
	Expect(err).NotTo(HaveOccurred())
	name := file.Name()
	defer func() { _ = os.Remove(name) }()
	_, err = file.WriteString(manifest)
	Expect(err).NotTo(HaveOccurred())
	Expect(file.Close()).To(Succeed())
	_, err = utils.Run(exec.Command("kubectl", "apply", "-f", name))
	Expect(err).NotTo(HaveOccurred())
}

type fakeProviderStateResponse struct {
	Owner      int `json:"owner"`
	RouteCount int `json:"routeCount"`
}

func fakeProviderState() fakeProviderStateResponse {
	portForward := exec.Command("kubectl", "port-forward", "service/fake-provider", "18080:8080", "-n", namespace)
	portForward.Stdout = io.Discard
	portForward.Stderr = io.Discard
	Expect(portForward.Start()).To(Succeed())
	defer func() { _ = portForward.Process.Kill() }()

	var state fakeProviderStateResponse
	Eventually(func(g Gomega) {
		response, err := http.Get("http://127.0.0.1:18080/state") // #nosec G107 -- fixed local E2E endpoint
		g.Expect(err).NotTo(HaveOccurred())
		defer func() { _ = response.Body.Close() }()
		g.Expect(response.StatusCode).To(Equal(http.StatusOK))
		g.Expect(json.NewDecoder(response.Body).Decode(&state)).To(Succeed())
	}, time.Minute, time.Second).Should(Succeed())
	return state
}

func failoverStatusFields(name string) (phase, attempted string) {
	phaseOutput, phaseErr := utils.Run(exec.Command("kubectl", "get", "failoverip", name, "-n", namespace, "-o", "jsonpath={.status.phase}"))
	Expect(phaseErr).NotTo(HaveOccurred())
	attemptedOutput, attemptedErr := utils.Run(exec.Command("kubectl", "get", "failoverip", name, "-n", namespace, "-o", "jsonpath={.status.lastAttemptedProviderMutationAt}"))
	Expect(attemptedErr).NotTo(HaveOccurred())
	return strings.TrimSpace(phaseOutput), strings.TrimSpace(attemptedOutput)
}

// serviceAccountToken returns a token for the specified service account in the given namespace.
// It uses the Kubernetes TokenRequest API to generate a token by directly sending a request
// and parsing the resulting token from the API response.
func serviceAccountToken() (string, error) {
	const tokenRequestRawString = `{
		"apiVersion": "authentication.k8s.io/v1",
		"kind": "TokenRequest"
	}`

	// Temporary file to store the token request
	secretName := fmt.Sprintf("%s-token-request", serviceAccountName)
	tokenRequestFile := filepath.Join("/tmp", secretName)
	err := os.WriteFile(tokenRequestFile, []byte(tokenRequestRawString), os.FileMode(0o644))
	if err != nil {
		return "", err
	}

	var out string
	verifyTokenCreation := func(g Gomega) {
		// Execute kubectl command to create the token
		cmd := exec.Command("kubectl", "create", "--raw", fmt.Sprintf(
			"/api/v1/namespaces/%s/serviceaccounts/%s/token",
			namespace,
			serviceAccountName,
		), "-f", tokenRequestFile)

		output, err := cmd.CombinedOutput()
		g.Expect(err).NotTo(HaveOccurred())

		// Parse the JSON output to extract the token
		var token tokenRequest
		err = json.Unmarshal(output, &token)
		g.Expect(err).NotTo(HaveOccurred())

		out = token.Status.Token
	}
	Eventually(verifyTokenCreation).Should(Succeed())

	return out, err
}

// getMetricsOutput retrieves and returns the logs from the curl pod used to access the metrics endpoint.
func getMetricsOutput() (string, error) {
	By("getting the curl-metrics logs")
	cmd := exec.Command("kubectl", "logs", "curl-metrics", "-n", namespace)
	return utils.Run(cmd)
}

// tokenRequest is a simplified representation of the Kubernetes TokenRequest API response,
// containing only the token field that we need to extract.
type tokenRequest struct {
	Status struct {
		Token string `json:"token"`
	} `json:"status"`
}
