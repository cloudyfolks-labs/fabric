package controller

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/util/retry"
	"k8s.io/klog/v2"

	"github.com/cloudyfolks-labs/fabric/pkg/util"
)

const (
	fabricTLSSecretName              = "fabric-tls" // #nosec G101 -- Kubernetes Secret resource name, not a credential.
	fabricTLSDefaultRotationInterval = 365 * 24 * time.Hour
	fabricTLSCADuration              = 10 * 365 * 24 * time.Hour
	fabricTLSCertDuration            = 10 * 365 * 24 * time.Hour
	fabricTLSCommonName              = "ovn"

	fabricTLSCertHashAnnotation        = "fabric.cloudyfolks.io/tls-cert-hash"
	legacyKubeOVNTLSCertHashAnnotation = "kube-ovn.io/kube-ovn-tls-cert-hash"
)

func (c *Controller) startFabricTLSManager(ctx context.Context) {
	if os.Getenv(util.EnvSSLEnabled) != "true" {
		return
	}
	interval, err := fabricTLSRotationInterval()
	if err != nil {
		klog.Errorf("failed to parse fabric TLS rotation interval: %v", err)
		return
	}
	if interval <= 0 {
		return
	}

	go wait.UntilWithContext(ctx, func(ctx context.Context) {
		if err := c.reconcileFabricTLS(ctx); err != nil {
			klog.Errorf("failed to reconcile fabric TLS secret: %v", err)
		}
	}, interval)
}

func fabricTLSRotationInterval() (time.Duration, error) {
	value := strings.TrimSpace(os.Getenv(util.EnvKubeOVNTLSRotationInterval))
	if value == "" {
		return fabricTLSDefaultRotationInterval, nil
	}
	interval, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("parse %s=%q: %w", util.EnvKubeOVNTLSRotationInterval, value, err)
	}
	return interval, nil
}

func (c *Controller) reconcileFabricTLS(ctx context.Context) error {
	secret, err := c.config.KubeClient.CoreV1().Secrets(c.config.PodNamespace).Get(ctx, fabricTLSSecretName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		data, hash, genErr := generateFabricTLSData(time.Now(), fabricTLSCADuration, fabricTLSCertDuration)
		if genErr != nil {
			return genErr
		}
		secret = &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      fabricTLSSecretName,
				Namespace: c.config.PodNamespace,
				Annotations: map[string]string{
					fabricTLSCertHashAnnotation: hash,
				},
			},
			Data: data,
		}
		if _, err = c.config.KubeClient.CoreV1().Secrets(c.config.PodNamespace).Create(ctx, secret, metav1.CreateOptions{}); err != nil {
			return err
		}
		return nil
	}
	if err != nil {
		return err
	}

	// fabric-tls keeps the legacy cacert/cert/key schema. The manager only
	// adds metadata on first adoption so upgrades do not restart OVN workloads.
	hash, err := fabricTLSHash(secret.Data)
	if err != nil {
		return err
	}

	if fabricTLSCertHash(secret) != hash {
		if err = c.setFabricTLSHash(ctx, hash); err != nil {
			return err
		}
		return nil
	}

	renew, err := fabricTLSNeedsRenewal(time.Now(), secret.Data)
	if err != nil {
		return err
	}
	if renew {
		data, newHash, genErr := generateFabricTLSData(time.Now(), fabricTLSCADuration, fabricTLSCertDuration)
		if genErr != nil {
			return genErr
		}
		if err = c.updateFabricTLSSecretData(ctx, data, newHash); err != nil {
			return err
		}
	}

	return nil
}

func fabricTLSNeedsRenewal(now time.Time, data map[string][]byte) (bool, error) {
	cert, err := parseFabricTLSCert(data)
	if err != nil {
		return false, err
	}
	refreshTime := cert.NotBefore.Add(cert.NotAfter.Sub(cert.NotBefore) / 2)
	return !now.Before(refreshTime), nil
}

func parseFabricTLSCert(data map[string][]byte) (*x509.Certificate, error) {
	cert, err := decodeCertificate(data["cert"])
	if err != nil {
		return nil, fmt.Errorf("parse fabric-tls cert: %w", err)
	}
	return cert, nil
}

func generateFabricTLSData(now time.Time, caDuration, certDuration time.Duration) (map[string][]byte, string, error) {
	data, err := util.GenerateFabricTLSSecretData(now, caDuration, certDuration, fabricTLSCommonName)
	if err != nil {
		return nil, "", err
	}
	hash, err := fabricTLSHash(data)
	if err != nil {
		return nil, "", err
	}
	return data, hash, nil
}

func fabricTLSHash(data map[string][]byte) (string, error) {
	for _, key := range []string{"cacert", "cert", "key"} {
		if len(data[key]) == 0 {
			return "", fmt.Errorf("fabric-tls missing %s", key)
		}
	}
	h := sha256.New()
	for _, key := range []string{"cacert", "cert", "key"} {
		h.Write([]byte(key))
		h.Write([]byte{0})
		h.Write(data[key])
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func (c *Controller) updateFabricTLSSecretData(ctx context.Context, data map[string][]byte, hash string) error {
	secrets := c.config.KubeClient.CoreV1().Secrets(c.config.PodNamespace)
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		secret, err := secrets.Get(ctx, fabricTLSSecretName, metav1.GetOptions{})
		if err != nil {
			return err
		}
		secret = secret.DeepCopy()
		secret.Data = data
		setFabricTLSAnnotation(secret, fabricTLSCertHashAnnotation, hash)
		_, err = secrets.Update(ctx, secret, metav1.UpdateOptions{})
		return err
	})
}

func (c *Controller) setFabricTLSHash(ctx context.Context, hash string) error {
	secrets := c.config.KubeClient.CoreV1().Secrets(c.config.PodNamespace)
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		secret, err := secrets.Get(ctx, fabricTLSSecretName, metav1.GetOptions{})
		if err != nil {
			return err
		}
		secret = secret.DeepCopy()
		setFabricTLSAnnotation(secret, fabricTLSCertHashAnnotation, hash)
		_, err = secrets.Update(ctx, secret, metav1.UpdateOptions{})
		return err
	})
}

func fabricTLSCertHash(secret *corev1.Secret) string {
	if hash := secret.Annotations[fabricTLSCertHashAnnotation]; hash != "" {
		return hash
	}
	return secret.Annotations[legacyKubeOVNTLSCertHashAnnotation]
}

func setFabricTLSAnnotation(secret *corev1.Secret, key, value string) {
	if secret.Annotations == nil {
		secret.Annotations = map[string]string{}
	}
	secret.Annotations[key] = value
	delete(secret.Annotations, legacyKubeOVNTLSCertHashAnnotation)
}
