package schedulerclient

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/utils/ptr"
)

func schedulerService(ports ...corev1.ServicePort) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "hami-scheduler", Namespace: "kube-system"},
		Spec:       corev1.ServiceSpec{Ports: ports},
	}
}

func schedulerSlice(ready bool, ip string) *discoveryv1.EndpointSlice {
	return &discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "hami-scheduler-abc",
			Namespace: "kube-system",
			Labels:    map[string]string{discoveryv1.LabelServiceName: "hami-scheduler"},
		},
		AddressType: discoveryv1.AddressTypeIPv4,
		Endpoints: []discoveryv1.Endpoint{{
			Addresses:  []string{ip},
			Conditions: discoveryv1.EndpointConditions{Ready: ptr.To(ready)},
		}},
	}
}

func TestDiscoverURL(t *testing.T) {
	https := corev1.ServicePort{Name: "http", Port: 443, TargetPort: intstr.FromInt32(9443)}
	for _, tc := range []struct {
		name       string
		objects    []runtime.Object
		want       string
		wantSource string
	}{
		{
			name:       "HAMi chart: monitor port targets named metrics port",
			objects:    []runtime.Object{schedulerService(https, corev1.ServicePort{Name: "monitor", Port: 31993, TargetPort: intstr.FromString("metrics")})},
			want:       "http://hami-scheduler.kube-system.svc:31993",
			wantSource: "service port",
		},
		{
			name:       "numeric target port",
			objects:    []runtime.Object{schedulerService(https, corev1.ServicePort{Name: "monitor", Port: 31993, TargetPort: intstr.FromInt32(9395)})},
			want:       "http://hami-scheduler.kube-system.svc:31993",
			wantSource: "service port",
		},
		{
			name:       "no metrics service port uses ready endpoint",
			objects:    []runtime.Object{schedulerService(https), schedulerSlice(true, "10.0.0.7")},
			want:       "http://10.0.0.7:9395",
			wantSource: "endpoint slice",
		},
		{
			name:       "not ready endpoint is skipped",
			objects:    []runtime.Object{schedulerService(https), schedulerSlice(false, "10.0.0.7")},
			want:       "http://hami-scheduler.kube-system.svc:9395",
			wantSource: "fallback",
		},
		{
			name:       "missing service",
			want:       "http://hami-scheduler.kube-system.svc:9395",
			wantSource: "fallback: get service",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := fake.NewSimpleClientset(tc.objects...)
			got, source := DiscoverURL(context.Background(), client, "kube-system", "hami-scheduler", 9395)
			if got != tc.want || !strings.HasPrefix(source, tc.wantSource) {
				t.Fatalf("got %s (%s), want %s (%s)", got, source, tc.want, tc.wantSource)
			}
		})
	}
}
