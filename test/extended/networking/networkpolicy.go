/*
Copyright 2016 The Kubernetes Authors.

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

package networking

import (
	kapiv1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/kubernetes/pkg/apis/networking"
	"k8s.io/kubernetes/test/e2e/framework"
	imageutils "k8s.io/kubernetes/test/utils/image"

	"fmt"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

// Forked version of vendor/k8s.io/kubernetes/test/e2e/network_policy.go that:
// 1. Uses InNetworkPolicyContext()
// 2. Uses IPs rather than DNS names in tests since our extended networking tests
//    don't run with DNS.
// 3. "Renames" the tests to specify "Feature:OSNetworkPolicy" to distinguish
//    them from the upstream ones.

/*
The following Network Policy tests verify that policy object definitions
are correctly enforced by a networking plugin. It accomplishes this by launching
a simple netcat server, and two clients with different
attributes. Each test case creates a network policy which should only allow
connections from one of the clients. The test then asserts that the clients
failed or succesfully connected as expected.
*/

var _ = Describe("NetworkPolicy", func() {
	InNetworkPolicyContext(func() {
		f := framework.NewDefaultFramework("network-policy")

		It("should support a 'default-deny' policy [Feature:OSNetworkPolicy]", func() {
			ns := f.Namespace

			By("Create a simple server.")
			podServer, service := createServerPodAndService(f, ns, "server", []int{80})
			defer cleanupServerPodAndService(f, podServer, service)
			framework.Logf("Waiting for Server to come up.")
			err := framework.WaitForPodRunningInNamespace(f.ClientSet, podServer)
			Expect(err).NotTo(HaveOccurred())

			// Create a pod with name 'client-a', which should be able to communicate with server.
			By("Creating client which will be able to contact the server since no policies are present.")
			testCanConnect(f, ns, "client-can-connect", service, 80)

			By("Creating a network policy denying all traffic.")
			policy := &networking.NetworkPolicy{
				ObjectMeta: metav1.ObjectMeta{
					Name: "deny-all",
				},
				Spec: networking.NetworkPolicySpec{
					PodSelector: metav1.LabelSelector{},
					Ingress:     []networking.NetworkPolicyIngressRule{},
				},
			}

			policy, err = f.InternalClientset.Networking().NetworkPolicies(ns.Name).Create(policy)
			Expect(err).NotTo(HaveOccurred())
			defer cleanupNetworkPolicy(f, policy)

			// Create a pod with name 'client-b', which will attempt to comunicate with the server,
			// but should not be able to now that isolation is on.
			testCannotConnect(f, ns, "client-cannot-connect", service, 80)
		})

		It("should enforce policy based on PodSelector [Feature:OSNetworkPolicy]", func() {
			ns := f.Namespace

			By("Creating a simple server.")
			serverPod, service := createServerPodAndService(f, ns, "server", []int{80})
			defer cleanupServerPodAndService(f, serverPod, service)
			framework.Logf("Waiting for Server to come up.")
			err := framework.WaitForPodRunningInNamespace(f.ClientSet, serverPod)
			Expect(err).NotTo(HaveOccurred())

			By("Creating a network policy for the server which allows traffic from the pod 'client-a'.")

			policy := &networking.NetworkPolicy{
				ObjectMeta: metav1.ObjectMeta{
					Name: "allow-client-a-via-pod-selector",
				},
				Spec: networking.NetworkPolicySpec{
					// Apply this policy to the Server
					PodSelector: metav1.LabelSelector{
						MatchLabels: map[string]string{
							"pod-name": serverPod.Name,
						},
					},
					// Allow traffic only from client-a
					Ingress: []networking.NetworkPolicyIngressRule{{
						From: []networking.NetworkPolicyPeer{{
							PodSelector: &metav1.LabelSelector{
								MatchLabels: map[string]string{
									"pod-name": "client-a",
								},
							},
						}},
					}},
				},
			}

			policy, err = f.InternalClientset.Networking().NetworkPolicies(ns.Name).Create(policy)

			Expect(err).NotTo(HaveOccurred())
			defer cleanupNetworkPolicy(f, policy)

			By("Creating client-a which should be able to contact the server.")
			testCanConnect(f, ns, "client-a", service, 80)
			testCannotConnect(f, ns, "client-b", service, 80)
		})

		It("should enforce policy based on Ports [Feature:OSNetworkPolicy]", func() {
			ns := f.Namespace

			// Create Server with Service
			By("Creating a simple server.")
			serverPod, service := createServerPodAndService(f, ns, "server", []int{80, 81})
			defer cleanupServerPodAndService(f, serverPod, service)
			framework.Logf("Waiting for Server to come up.")
			err := framework.WaitForPodRunningInNamespace(f.ClientSet, serverPod)
			Expect(err).NotTo(HaveOccurred())

			By("Testing pods can connect to both ports when no policy is present.")
			testCanConnect(f, ns, "basecase-reachable-80", service, 80)
			testCanConnect(f, ns, "basecase-reachable-81", service, 81)

			By("Creating a network policy for the Service which allows traffic only to one port.")
			policy := &networking.NetworkPolicy{
				ObjectMeta: metav1.ObjectMeta{
					Name: "allow-ingress-on-port-81",
				},
				Spec: networking.NetworkPolicySpec{
					// Apply to server
					PodSelector: metav1.LabelSelector{
						MatchLabels: map[string]string{
							"pod-name": serverPod.Name,
						},
					},
					// Allow traffic only to one port.
					Ingress: []networking.NetworkPolicyIngressRule{{
						Ports: []networking.NetworkPolicyPort{{
							Port: &intstr.IntOrString{IntVal: 81},
						}},
					}},
				},
			}
			policy, err = f.InternalClientset.Networking().NetworkPolicies(ns.Name).Create(policy)

			Expect(err).NotTo(HaveOccurred())
			defer cleanupNetworkPolicy(f, policy)

			By("Testing pods can connect only to the port allowed by the policy.")
			testCannotConnect(f, ns, "client-a", service, 80)
			testCanConnect(f, ns, "client-b", service, 81)
		})

		It("should enforce multiple, stacked policies with overlapping podSelectors [Feature:OSNetworkPolicy]", func() {
			ns := f.Namespace

			// Create Server with Service
			By("Creating a simple server.")
			serverPod, service := createServerPodAndService(f, ns, "server", []int{80, 81})
			defer cleanupServerPodAndService(f, serverPod, service)
			framework.Logf("Waiting for Server to come up.")
			err := framework.WaitForPodRunningInNamespace(f.ClientSet, serverPod)
			Expect(err).NotTo(HaveOccurred())

			By("Testing pods can connect to both ports when no policy is present.")
			testCanConnect(f, ns, "test-a", service, 80)
			testCanConnect(f, ns, "test-b", service, 81)

			By("Creating a network policy for the Service which allows traffic only to one port.")
			policy := &networking.NetworkPolicy{
				ObjectMeta: metav1.ObjectMeta{
					Name: "allow-ingress-on-port-80",
				},
				Spec: networking.NetworkPolicySpec{
					// Apply to server
					PodSelector: metav1.LabelSelector{
						MatchLabels: map[string]string{
							"pod-name": serverPod.Name,
						},
					},
					// Allow traffic only to one port.
					Ingress: []networking.NetworkPolicyIngressRule{{
						Ports: []networking.NetworkPolicyPort{{
							Port: &intstr.IntOrString{IntVal: 80},
						}},
					}},
				},
			}
			policy, err = f.InternalClientset.Networking().NetworkPolicies(ns.Name).Create(policy)
			Expect(err).NotTo(HaveOccurred())
			defer cleanupNetworkPolicy(f, policy)

			By("Creating a network policy for the Service which allows traffic only to another port.")
			policy2 := &networking.NetworkPolicy{
				ObjectMeta: metav1.ObjectMeta{
					Name: "allow-ingress-on-port-81",
				},
				Spec: networking.NetworkPolicySpec{
					// Apply to server
					PodSelector: metav1.LabelSelector{
						MatchLabels: map[string]string{
							"pod-name": serverPod.Name,
						},
					},
					// Allow traffic only to one port.
					Ingress: []networking.NetworkPolicyIngressRule{{
						Ports: []networking.NetworkPolicyPort{{
							Port: &intstr.IntOrString{IntVal: 81},
						}},
					}},
				},
			}
			policy2, err = f.InternalClientset.Networking().NetworkPolicies(ns.Name).Create(policy2)
			Expect(err).NotTo(HaveOccurred())
			defer cleanupNetworkPolicy(f, policy2)

			By("Testing pods can connect to both ports when both policies are present.")
			testCanConnect(f, ns, "client-a", service, 80)
			testCanConnect(f, ns, "client-b", service, 81)
		})

		It("should support allow-all policy [Feature:OSNetworkPolicy]", func() {
			ns := f.Namespace

			// Create Server with Service
			By("Creating a simple server.")
			serverPod, service := createServerPodAndService(f, ns, "server", []int{80, 81})
			defer cleanupServerPodAndService(f, serverPod, service)
			framework.Logf("Waiting for Server to come up.")
			err := framework.WaitForPodRunningInNamespace(f.ClientSet, serverPod)
			Expect(err).NotTo(HaveOccurred())

			By("Testing pods can connect to both ports when no policy is present.")
			testCanConnect(f, ns, "test-a", service, 80)
			testCanConnect(f, ns, "test-b", service, 81)

			By("Creating a network policy which allows all traffic.")
			policy := &networking.NetworkPolicy{
				ObjectMeta: metav1.ObjectMeta{
					Name: "allow-all",
				},
				Spec: networking.NetworkPolicySpec{
					// Allow all traffic
					PodSelector: metav1.LabelSelector{
						MatchLabels: map[string]string{},
					},
					Ingress: []networking.NetworkPolicyIngressRule{{}},
				},
			}
			policy, err = f.InternalClientset.Networking().NetworkPolicies(ns.Name).Create(policy)
			Expect(err).NotTo(HaveOccurred())
			defer cleanupNetworkPolicy(f, policy)

			By("Testing pods can connect to both ports when an 'allow-all' policy is present.")
			testCanConnect(f, ns, "client-a", service, 80)
			testCanConnect(f, ns, "client-b", service, 81)
		})

		It("should enforce policy based on NamespaceSelector [Feature:OSNetworkPolicy]", func() {
			nsA := f.Namespace
			nsBName := f.BaseName + "-b"
			// The CreateNamespace helper uses the input name as a Name Generator, so the namespace itself
			// will have a different name than what we are setting as the value of ns-name.
			// This is fine as long as we don't try to match the label as nsB.Name in our policy.
			nsB, err := f.CreateNamespace(nsBName, map[string]string{
				"ns-name": nsBName,
			})
			Expect(err).NotTo(HaveOccurred())

			// Create Server with Service in NS-B
			By("Creating a webserver tied to a service.")
			serverPod, service := createServerPodAndService(f, nsA, "server", []int{80})
			defer cleanupServerPodAndService(f, serverPod, service)
			framework.Logf("Waiting for server to come up.")
			err = framework.WaitForPodRunningInNamespace(f.ClientSet, serverPod)
			Expect(err).NotTo(HaveOccurred())

			// Create Policy for that service that allows traffic only via namespace B
			By("Creating a network policy for the server which allows traffic from namespace-b.")
			policy := &networking.NetworkPolicy{
				ObjectMeta: metav1.ObjectMeta{
					Name: "allow-ns-b-via-namespace-selector",
				},
				Spec: networking.NetworkPolicySpec{
					// Apply to server
					PodSelector: metav1.LabelSelector{
						MatchLabels: map[string]string{
							"pod-name": serverPod.Name,
						},
					},
					// Allow traffic only from NS-B
					Ingress: []networking.NetworkPolicyIngressRule{{
						From: []networking.NetworkPolicyPeer{{
							NamespaceSelector: &metav1.LabelSelector{
								MatchLabels: map[string]string{
									"ns-name": nsBName,
								},
							},
						}},
					}},
				},
			}
			policy, err = f.InternalClientset.Networking().NetworkPolicies(nsA.Name).Create(policy)
			Expect(err).NotTo(HaveOccurred())
			defer cleanupNetworkPolicy(f, policy)

			testCannotConnect(f, nsA, "client-a", service, 80)
			testCanConnect(f, nsB, "client-b", service, 80)
		})
	})
})

func testCanConnect(f *framework.Framework, ns *kapiv1.Namespace, podName string, service *kapiv1.Service, targetPort int) {
	By(fmt.Sprintf("Creating client pod %s that should successfully connect to %s.", podName, service.Name))
	podClient := createNetworkClientPod(f, ns, podName, service.Spec.ClusterIP, targetPort)
	defer func() {
		By(fmt.Sprintf("Cleaning up the pod %s", podName))
		if err := f.ClientSet.CoreV1().Pods(ns.Name).Delete(podClient.Name, nil); err != nil {
			framework.Failf("unable to cleanup pod %v: %v", podClient.Name, err)
		}
	}()

	framework.Logf("Waiting for %s to complete.", podClient.Name)
	err := framework.WaitForPodNoLongerRunningInNamespace(f.ClientSet, podClient.Name, ns.Name)
	Expect(err).NotTo(HaveOccurred(), "Pod did not finish as expected.")

	framework.Logf("Waiting for %s to complete.", podClient.Name)
	err = framework.WaitForPodSuccessInNamespace(f.ClientSet, podClient.Name, ns.Name)
	if err != nil {
		// Collect pod logs when we see a failure.
		logs, logErr := framework.GetPodLogs(f.ClientSet, f.Namespace.Name, podName, fmt.Sprintf("%s-container", podName))
		if logErr != nil {
			framework.Failf("Error getting container logs: %s", logErr)
		}

		// Collect current NetworkPolicies applied in the test namespace.
		policies, err := f.InternalClientset.Networking().NetworkPolicies(f.Namespace.Name).List(metav1.ListOptions{})
		if err != nil {
			framework.Logf("error getting current NetworkPolicies for %s namespace: %s", f.Namespace.Name, err)
		}

		// Collect the list of pods running in the test namespace.
		podsInNS, err := framework.GetPodsInNamespace(f.ClientSet, f.Namespace.Name, map[string]string{})
		if err != nil {
			framework.Logf("error getting pods for %s namespace: %s", f.Namespace.Name, err)
		}

		pods := []string{}
		for _, p := range podsInNS {
			pods = append(pods, fmt.Sprintf("Pod: %s, Status: %s\n", p.Name, p.Status.String()))
		}

		framework.Failf("Pod %s should be able to connect to service %s, but was not able to connect.\nPod logs:\n%s\n\n Current NetworkPolicies:\n\t%v\n\n Pods:\n\t%v\n\n", podName, service.Name, logs, policies.Items, pods)

		// Dump debug information for the test namespace.
		framework.DumpDebugInfo(f.ClientSet, f.Namespace.Name)
	}
}

func testCannotConnect(f *framework.Framework, ns *kapiv1.Namespace, podName string, service *kapiv1.Service, targetPort int) {
	By(fmt.Sprintf("Creating client pod %s that should not be able to connect to %s.", podName, service.Name))
	podClient := createNetworkClientPod(f, ns, podName, service.Spec.ClusterIP, targetPort)
	defer func() {
		By(fmt.Sprintf("Cleaning up the pod %s", podName))
		if err := f.ClientSet.CoreV1().Pods(ns.Name).Delete(podClient.Name, nil); err != nil {
			framework.Failf("unable to cleanup pod %v: %v", podClient.Name, err)
		}
	}()

	framework.Logf("Waiting for %s to complete.", podClient.Name)
	err := framework.WaitForPodSuccessInNamespace(f.ClientSet, podClient.Name, ns.Name)

	// We expect an error here since it's a cannot connect test.
	// Dump debug information if the error was nil.
	if err == nil {
		// Collect pod logs when we see a failure.
		logs, logErr := framework.GetPodLogs(f.ClientSet, f.Namespace.Name, podName, fmt.Sprintf("%s-container", podName))
		if logErr != nil {
			framework.Failf("Error getting container logs: %s", logErr)
		}

		// Collect current NetworkPolicies applied in the test namespace.
		policies, err := f.InternalClientset.Networking().NetworkPolicies(f.Namespace.Name).List(metav1.ListOptions{})
		if err != nil {
			framework.Logf("error getting current NetworkPolicies for %s namespace: %s", f.Namespace.Name, err)
		}

		// Collect the list of pods running in the test namespace.
		podsInNS, err := framework.GetPodsInNamespace(f.ClientSet, f.Namespace.Name, map[string]string{})
		if err != nil {
			framework.Logf("error getting pods for %s namespace: %s", f.Namespace.Name, err)
		}

		pods := []string{}
		for _, p := range podsInNS {
			pods = append(pods, fmt.Sprintf("Pod: %s, Status: %s\n", p.Name, p.Status.String()))
		}

		framework.Failf("Pod %s should not be able to connect to service %s, but was able to connect.\nPod logs:\n%s\n\n Current NetworkPolicies:\n\t%v\n\n Pods:\n\t %v\n\n", podName, service.Name, logs, policies.Items, pods)

		// Dump debug information for the test namespace.
		framework.DumpDebugInfo(f.ClientSet, f.Namespace.Name)
	}
}

// Create a server pod with a listening container for each port in ports[].
// Will also assign a pod label with key: "pod-name" and label set to the given podname for later use by the network
// policy.
func createServerPodAndService(f *framework.Framework, namespace *kapiv1.Namespace, podName string, ports []int) (*kapiv1.Pod, *kapiv1.Service) {
	// Because we have a variable amount of ports, we'll first loop through and generate our Containers for our pod,
	// and ServicePorts.for our Service.
	containers := []kapiv1.Container{}
	servicePorts := []kapiv1.ServicePort{}
	for _, port := range ports {
		// Build the containers for the server pod.
		containers = append(containers, kapiv1.Container{
			Name:  fmt.Sprintf("%s-container-%d", podName, port),
			Image: imageutils.GetE2EImage(imageutils.Porter),
			Env: []kapiv1.EnvVar{
				{
					Name:  fmt.Sprintf("SERVE_PORT_%d", port),
					Value: "foo",
				},
			},
			Ports: []kapiv1.ContainerPort{
				{
					ContainerPort: int32(port),
					Name:          fmt.Sprintf("serve-%d", port),
				},
			},
		})

		// Build the Service Ports for the service.
		servicePorts = append(servicePorts, kapiv1.ServicePort{
			Name:       fmt.Sprintf("%s-%d", podName, port),
			Port:       int32(port),
			TargetPort: intstr.FromInt(port),
		})
	}

	By(fmt.Sprintf("Creating a server pod %s in namespace %s", podName, namespace.Name))
	pod, err := f.ClientSet.CoreV1().Pods(namespace.Name).Create(&kapiv1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: podName,
			Labels: map[string]string{
				"pod-name": podName,
			},
		},
		Spec: kapiv1.PodSpec{
			Containers:    containers,
			RestartPolicy: kapiv1.RestartPolicyNever,
		},
	})
	Expect(err).NotTo(HaveOccurred())
	framework.Logf("Created pod %v", pod.ObjectMeta.Name)

	svcName := fmt.Sprintf("svc-%s", podName)
	By(fmt.Sprintf("Creating a service %s for pod %s in namespace %s", svcName, podName, namespace.Name))
	svc, err := f.ClientSet.CoreV1().Services(namespace.Name).Create(&kapiv1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name: svcName,
		},
		Spec: kapiv1.ServiceSpec{
			Ports: servicePorts,
			Selector: map[string]string{
				"pod-name": podName,
			},
		},
	})
	Expect(err).NotTo(HaveOccurred())
	framework.Logf("Created service %s", svc.Name)

	return pod, svc
}

func cleanupServerPodAndService(f *framework.Framework, pod *kapiv1.Pod, service *kapiv1.Service) {
	By("Cleaning up the server.")
	if err := f.ClientSet.Core().Pods(pod.Namespace).Delete(pod.Name, nil); err != nil {
		framework.Failf("unable to cleanup pod %v: %v", pod.Name, err)
	}
	By("Cleaning up the server's service.")
	if err := f.ClientSet.Core().Services(service.Namespace).Delete(service.Name, nil); err != nil {
		framework.Failf("unable to cleanup svc %v: %v", service.Name, err)
	}
}

// Create a client pod which will attempt a netcat to the provided service, on the specified port.
// This client will attempt a oneshot connection, then die, without restarting the pod.
// Test can then be asserted based on whether the pod quit with an error or not.
func createNetworkClientPod(f *framework.Framework, namespace *kapiv1.Namespace, podName string, targetIP string, targetPort int) *kapiv1.Pod {
	pod, err := f.ClientSet.CoreV1().Pods(namespace.Name).Create(&kapiv1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: podName,
			Labels: map[string]string{
				"pod-name": podName,
			},
		},
		Spec: kapiv1.PodSpec{
			RestartPolicy: kapiv1.RestartPolicyNever,
			Containers: []kapiv1.Container{
				{
					Name:  fmt.Sprintf("%s-container", podName),
					Image: "busybox",
					Args: []string{
						"/bin/sh",
						"-c",
						fmt.Sprintf("for i in $(seq 1 5); do wget -T 8 %s:%d -O - && exit 0 || sleep 1; done; exit 1",
							targetIP, targetPort),
					},
				},
			},
		},
	})

	Expect(err).NotTo(HaveOccurred())
	return pod
}

func cleanupNetworkPolicy(f *framework.Framework, policy *networking.NetworkPolicy) {
	By("Cleaning up the policy.")
	if err := f.InternalClientset.Networking().NetworkPolicies(policy.Namespace).Delete(policy.Name, nil); err != nil {
		framework.Failf("unable to cleanup policy %v: %v", policy.Name, err)
	}
}
