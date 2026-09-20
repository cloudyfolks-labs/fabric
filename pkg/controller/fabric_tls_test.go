package controller

import (
	"bytes"
	"context"
	"crypto/x509"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/cloudyfolks-labs/fabric/pkg/util"
)

func TestReconcileFabricTLSAddsBaselineAnnotation(t *testing.T) {
	const namespace = "kube-system"
	data, hash, err := generateFabricTLSData(time.Now(), fabricTLSCADuration, fabricTLSCertDuration)
	if err != nil {
		t.Fatalf("generateFabricTLSData returned error: %v", err)
	}
	client := fake.NewSimpleClientset(
		testFabricTLSSecret(namespace, data, nil),
	)
	c := &Controller{config: &Configuration{KubeClient: client, PodNamespace: namespace}}

	if err = c.reconcileFabricTLS(context.Background()); err != nil {
		t.Fatalf("reconcileFabricTLS returned error: %v", err)
	}

	secret, err := client.CoreV1().Secrets(namespace).Get(context.Background(), fabricTLSSecretName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("failed to get fabric-tls secret: %v", err)
	}
	if got := secret.Annotations[fabricTLSCertHashAnnotation]; got != hash {
		t.Fatalf("cert hash annotation = %q, want %q", got, hash)
	}
}

func TestReconcileFabricTLSOnlyAdoptsExpiredLegacySecret(t *testing.T) {
	const namespace = "kube-system"
	expiredData, hash, err := generateFabricTLSData(time.Now().Add(-20*24*time.Hour), 10*24*time.Hour, 10*24*time.Hour)
	if err != nil {
		t.Fatalf("generateFabricTLSData returned error: %v", err)
	}
	client := fake.NewSimpleClientset(
		testFabricTLSSecret(namespace, expiredData, nil),
	)
	c := &Controller{config: &Configuration{KubeClient: client, PodNamespace: namespace}}

	if err = c.reconcileFabricTLS(context.Background()); err != nil {
		t.Fatalf("reconcileFabricTLS returned error: %v", err)
	}

	secret, err := client.CoreV1().Secrets(namespace).Get(context.Background(), fabricTLSSecretName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("failed to get fabric-tls secret: %v", err)
	}
	if got := secret.Annotations[fabricTLSCertHashAnnotation]; got != hash {
		t.Fatalf("cert hash annotation = %q, want %q", got, hash)
	}
	assertSecretDataEqual(t, secret.Data, expiredData)
}

func TestReconcileFabricTLSRotatesExpiredSecret(t *testing.T) {
	const namespace = "kube-system"
	expiredData, oldHash, err := generateFabricTLSData(time.Now().Add(-20*24*time.Hour), 10*24*time.Hour, 10*24*time.Hour)
	if err != nil {
		t.Fatalf("generateFabricTLSData returned error: %v", err)
	}
	client := fake.NewSimpleClientset(
		testFabricTLSSecret(namespace, expiredData, map[string]string{
			fabricTLSCertHashAnnotation: oldHash,
		}),
	)
	c := &Controller{config: &Configuration{KubeClient: client, PodNamespace: namespace}}

	if err = c.reconcileFabricTLS(context.Background()); err != nil {
		t.Fatalf("reconcileFabricTLS returned error: %v", err)
	}

	secret, err := client.CoreV1().Secrets(namespace).Get(context.Background(), fabricTLSSecretName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("failed to get fabric-tls secret: %v", err)
	}
	newHash := secret.Annotations[fabricTLSCertHashAnnotation]
	if newHash == "" || newHash == oldHash {
		t.Fatalf("cert hash annotation = %q, want non-empty value different from %q", newHash, oldHash)
	}
}

func TestGenerateFabricTLSDataAddsServingAndClientIdentity(t *testing.T) {
	data, _, err := generateFabricTLSData(time.Now(), fabricTLSCADuration, fabricTLSCertDuration)
	if err != nil {
		t.Fatalf("generateFabricTLSData returned error: %v", err)
	}

	cert, err := parseFabricTLSCert(data)
	if err != nil {
		t.Fatalf("parseFabricTLSCert returned error: %v", err)
	}

	if !containsExtKeyUsage(cert.ExtKeyUsage, x509.ExtKeyUsageServerAuth) {
		t.Fatalf("leaf certificate ExtKeyUsage = %v, want ServerAuth", cert.ExtKeyUsage)
	}
	if !containsExtKeyUsage(cert.ExtKeyUsage, x509.ExtKeyUsageClientAuth) {
		t.Fatalf("leaf certificate ExtKeyUsage = %v, want ClientAuth", cert.ExtKeyUsage)
	}
	if !containsString(cert.DNSNames, fabricTLSCommonName) {
		t.Fatalf("leaf certificate DNSNames = %v, want %q", cert.DNSNames, fabricTLSCommonName)
	}
}

func TestStartFabricTLSManagerPeriodicallyRotatesExpiredSecret(t *testing.T) {
	const namespace = "kube-system"
	expiredData, oldHash, err := generateFabricTLSData(time.Now().Add(-20*24*time.Hour), 10*24*time.Hour, 10*24*time.Hour)
	if err != nil {
		t.Fatalf("generateFabricTLSData returned error: %v", err)
	}
	client := fake.NewSimpleClientset(
		testFabricTLSSecret(namespace, expiredData, map[string]string{
			fabricTLSCertHashAnnotation: oldHash,
		}),
	)
	c := &Controller{config: &Configuration{KubeClient: client, PodNamespace: namespace}}
	t.Setenv(util.EnvSSLEnabled, "true")
	t.Setenv(util.EnvKubeOVNTLSRotationInterval, "10ms")

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	c.startFabricTLSManager(ctx)

	err = wait.PollUntilContextTimeout(context.Background(), 10*time.Millisecond, 2*time.Second, true, func(ctx context.Context) (bool, error) {
		secret, err := client.CoreV1().Secrets(namespace).Get(ctx, fabricTLSSecretName, metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		newHash, err := fabricTLSHash(secret.Data)
		if err != nil {
			return false, err
		}
		return newHash != oldHash && secret.Annotations[fabricTLSCertHashAnnotation] == newHash, nil
	})
	if err != nil {
		t.Fatalf("startFabricTLSManager did not rotate expired fabric-tls secret: %v", err)
	}
}

func TestFabricTLSRotationInterval(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		want    time.Duration
		wantErr bool
	}{
		{name: "empty uses default", value: "", want: 365 * 24 * time.Hour},
		{name: "zero disables rotation", value: "0", want: 0},
		{name: "custom interval", value: "12h", want: 12 * time.Hour},
		{name: "invalid interval", value: "bad", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("KUBE_OVN_TLS_ROTATION_INTERVAL", tt.value)
			got, err := fabricTLSRotationInterval()
			if tt.wantErr {
				if err == nil {
					t.Fatal("fabricTLSRotationInterval returned nil error, want error")
				}
				return
			}
			if err != nil {
				t.Fatalf("fabricTLSRotationInterval returned error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("interval = %v, want %v", got, tt.want)
			}
		})
	}
}

func assertSecretDataEqual(t *testing.T, got, want map[string][]byte) {
	t.Helper()
	for _, key := range []string{"cacert", "cert", "key"} {
		if !bytes.Equal(got[key], want[key]) {
			t.Fatalf("secret data %s changed during adoption", key)
		}
	}
}

func containsExtKeyUsage(usages []x509.ExtKeyUsage, want x509.ExtKeyUsage) bool {
	return slices.Contains(usages, want)
}

func containsString(values []string, want string) bool {
	return slices.Contains(values, want)
}

func testFabricTLSSecret(namespace string, data map[string][]byte, annotations map[string]string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:        fabricTLSSecretName,
			Namespace:   namespace,
			Annotations: annotations,
		},
		Data: data,
	}
}

func TestFabricTLSCertHashReadsLegacyAnnotation(t *testing.T) {
	t.Parallel()

	legacy := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
		Annotations: map[string]string{legacyKubeOVNTLSCertHashAnnotation: "old"},
	}}
	require.Equal(t, "old", fabricTLSCertHash(legacy))

	both := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
		Annotations: map[string]string{
			legacyKubeOVNTLSCertHashAnnotation: "old",
			fabricTLSCertHashAnnotation:        "new",
		},
	}}
	require.Equal(t, "new", fabricTLSCertHash(both))

	setFabricTLSAnnotation(both, fabricTLSCertHashAnnotation, "newer")
	require.Equal(t, "newer", both.Annotations[fabricTLSCertHashAnnotation])
	require.NotContains(t, both.Annotations, legacyKubeOVNTLSCertHashAnnotation)
}
