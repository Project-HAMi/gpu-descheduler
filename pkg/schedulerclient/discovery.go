/*
Copyright 2024 The HAMi Authors.

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

package schedulerclient

import (
	"context"
	"fmt"
	"net"
	"strconv"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// metricsPortName is the container port name the HAMi chart gives the scheduler metrics port.
const metricsPortName = "metrics"

// DiscoverURL returns the base URL of the HAMi scheduler metrics endpoint and
// how it was found.
//
// It prefers the scheduler Service, using the Service port that targets the
// metrics port. The HAMi chart exposes metrics on a different Service port
// (31993 by default) and, with leader election, routes only to the leader, so
// the Service follows leader changes. If no Service port matches, it uses the
// first ready endpoint of the Service. If both fail, it falls back to the
// Service DNS name with metricsPort.
func DiscoverURL(ctx context.Context, client kubernetes.Interface, namespace, service string, metricsPort int) (string, string) {
	fallback := fmt.Sprintf("http://%s.%s.svc:%d", service, namespace, metricsPort)
	if client == nil {
		return fallback, "fallback: no Kubernetes client"
	}

	svc, err := client.CoreV1().Services(namespace).Get(ctx, service, metav1.GetOptions{})
	if err == nil {
		if port, ok := metricsServicePort(svc, metricsPort); ok {
			return fmt.Sprintf("http://%s.%s.svc:%d", service, namespace, port), "service port"
		}
	}

	slices, sliceErr := client.DiscoveryV1().EndpointSlices(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: discoveryv1.LabelServiceName + "=" + service,
	})
	if sliceErr == nil {
		for _, slice := range slices.Items {
			for _, ep := range slice.Endpoints {
				if ep.Conditions.Ready != nil && !*ep.Conditions.Ready {
					continue
				}
				if len(ep.Addresses) > 0 {
					hostPort := net.JoinHostPort(ep.Addresses[0], strconv.Itoa(metricsPort))
					return "http://" + hostPort, "endpoint slice"
				}
			}
		}
	}

	switch {
	case err != nil:
		return fallback, fmt.Sprintf("fallback: get service: %v", err)
	case sliceErr != nil:
		return fallback, fmt.Sprintf("fallback: list endpoint slices: %v", sliceErr)
	default:
		return fallback, "fallback: no service port or ready endpoint for the metrics port"
	}
}

// metricsServicePort finds the Service port that forwards to the scheduler metrics port.
func metricsServicePort(svc *corev1.Service, metricsPort int) (int32, bool) {
	for _, p := range svc.Spec.Ports {
		target := p.TargetPort
		if target.StrVal == metricsPortName || (target.StrVal == "" && int(target.IntVal) == metricsPort) {
			return p.Port, true
		}
	}
	for _, p := range svc.Spec.Ports {
		if int(p.Port) == metricsPort && p.TargetPort.StrVal == "" && p.TargetPort.IntVal == 0 {
			return p.Port, true
		}
	}
	return 0, false
}
