package dnsnameresolver

import (
	"fmt"
	"sync"
	"time"

	"github.com/coredns/coredns/plugin"

	kubeovnclient "github.com/kubeovn/kube-ovn/pkg/client/clientset/versioned"
	kubeovnclientv1 "github.com/kubeovn/kube-ovn/pkg/client/clientset/versioned/typed/kubeovn/v1"
	kubeovninformer "github.com/kubeovn/kube-ovn/pkg/client/informers/externalversions"
	kubeovnlister "github.com/kubeovn/kube-ovn/pkg/client/listers/kubeovn/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
)

// DNSNameResolver is a plugin that looks up responses from other plugins
// and updates the status of DNSNameResolver objects.
type DNSNameResolver struct {
	Next plugin.Handler

	// client and informer for handling DNSNameResolver objects.
	kubeovnClient           kubeovnclientv1.FabricV1Interface
	dnsNameResolverInformer cache.SharedIndexInformer
	dnsNameResolverLister   kubeovnlister.DNSNameResolverLister
	stopCh                  chan struct{}
	stopLock                sync.Mutex
	shutdown                bool
}

// New returns an initialized DNSNameResolver with default settings.
func New() *DNSNameResolver {
	return &DNSNameResolver{}
}

const (
	// defaultResyncPeriod gives the resync period used for creating the DNSNameResolver informer.
	defaultResyncPeriod = 24 * time.Hour
	// defaultMinTTL is the minimum TTL for DNS records.
	defaultMinTTL int32 = 5
	// defaultFailureThreshold is the number of failures before removing a resolved name.
	defaultFailureThreshold int32 = 5
)

// initInformer initializes the DNSNameResolver informer.
func (resolver *DNSNameResolver) initInformer(networkClient kubeovnclient.Interface) (err error) {
	// Get the client for version v1alpha1 for DNSNameResolver objects.
	resolver.kubeovnClient = networkClient.FabricV1()

	// Create the DNSNameResolver informer.
	resolver.dnsNameResolverInformer = kubeovninformer.NewSharedInformerFactory(networkClient, defaultResyncPeriod).Fabric().V1().DNSNameResolvers().Informer()

	// Get the lister for DNSNameResolver objects.
	resolver.dnsNameResolverLister = kubeovnlister.NewDNSNameResolverLister(resolver.dnsNameResolverInformer.GetIndexer())

	// No need for event handlers since we query lister directly
	return nil
}

// initPlugin initializes the dnsnameresolver plugin and returns the plugin startup and
// shutdown callback functions.
func (resolver *DNSNameResolver) initPlugin() (func() error, func() error, error) {
	// Create a client supporting kube-ovn apis.
	kubeConfig, err := rest.InClusterConfig()
	if err != nil {
		return nil, nil, err
	}
	cfg := rest.CopyConfig(kubeConfig)
	cfg.UserAgent = "coredns-dnsnameresolver/1.0"
	cfg.QPS, cfg.Burst = 30, 60
	networkClient, err := kubeovnclient.NewForConfig(cfg)
	if err != nil {
		return nil, nil, err
	}

	if err = resolver.initInformer(networkClient); err != nil {
		return nil, nil, err
	}

	resolver.stopCh = make(chan struct{})

	onStart := func() error {
		go func() {
			log.Info("Starting DNS Name Resolver Informer")
			resolver.dnsNameResolverInformer.Run(resolver.stopCh)
		}()

		timeout := 5 * time.Second
		timeoutTicker := time.NewTicker(timeout)
		defer timeoutTicker.Stop()
		logDelay := 500 * time.Millisecond
		logTicker := time.NewTicker(logDelay)
		defer logTicker.Stop()
		checkSyncTicker := time.NewTicker(100 * time.Millisecond)
		defer checkSyncTicker.Stop()
		for {
			select {
			case <-checkSyncTicker.C:
				if resolver.dnsNameResolverInformer.HasSynced() {
					log.Info("DNS Name Resolver Informer synced successfully")
					return nil
				}
			case <-logTicker.C:
				log.Info("waiting for DNS Name Resolver Informer sync before starting server")
			case <-timeoutTicker.C:
				// Following similar strategy of the kubernetes CoreDNS plugin to start the server
				// with unsynced informer. For reference:
				// https://github.com/openshift/coredns/blob/022a0530038602605b8f3e8866c2a6ded97708cc/plugin/kubernetes/kubernetes.go#L261-L287
				log.Warning("starting server with unsynced DNS Name Resolver Informer")
				return nil
			}
		}
	}

	onShut := func() error {
		resolver.stopLock.Lock()
		defer resolver.stopLock.Unlock()

		// Only try draining the workqueue if we haven't already.
		if !resolver.shutdown {
			close(resolver.stopCh)
			resolver.shutdown = true

			return nil
		}

		return fmt.Errorf("shutdown already in progress")
	}

	return onStart, onShut, nil
}
