package controller

import (
	"context"
	"net"
	"slices"
	"strings"
	"sync"
	"time"

	"k8s.io/klog/v2"

	kubeovnv1 "github.com/cloudyfolks-labs/fabric/pkg/apis/kubeovn/v1"
	"github.com/cloudyfolks-labs/fabric/pkg/util"
)

const (
	domainResolveInterval = 5 * time.Second
	domainResolveTimeout  = 5 * time.Second
)

type domainEntry struct {
	policies map[string]struct{}
	v4       []string
	v6       []string
}

type domainResolver struct {
	mu      sync.Mutex
	domains map[string]*domainEntry
	lookup  func(ctx context.Context, host string) ([]net.IPAddr, error)
	notify  func(policy string)
}

func newDomainResolver(dnsServer func() string, notify func(policy string)) *domainResolver {
	fallback := &net.Resolver{}
	resolver := &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			if server := dnsServer(); server != "" {
				address = server
			}
			var dialer net.Dialer
			return dialer.DialContext(ctx, network, address)
		},
	}
	return &domainResolver{
		domains: make(map[string]*domainEntry),
		lookup: func(ctx context.Context, host string) ([]net.IPAddr, error) {
			addrs, err := resolver.LookupIPAddr(ctx, host)
			if err != nil && dnsServer() == "" {
				return fallback.LookupIPAddr(ctx, host)
			}
			return addrs, err
		},
		notify: notify,
	}
}

func normalizeDomain(domain string) (string, bool) {
	domain = strings.TrimSuffix(domain, ".")
	if strings.HasPrefix(domain, "*.") {
		return "", false
	}
	return domain, domain != ""
}

func (r *domainResolver) setPolicyDomains(policy string, domains []string) {
	desired := make(map[string]struct{}, len(domains))
	for _, domain := range domains {
		normalized, ok := normalizeDomain(domain)
		if !ok {
			klog.Warningf("policy %s: domain %q is not resolvable without query interception, only exact fqdns are supported", policy, domain)
			continue
		}
		desired[normalized] = struct{}{}
	}

	var added []string
	r.mu.Lock()
	for domain, entry := range r.domains {
		if _, ok := desired[domain]; ok {
			continue
		}
		if _, ok := entry.policies[policy]; !ok {
			continue
		}
		delete(entry.policies, policy)
		if len(entry.policies) == 0 {
			delete(r.domains, domain)
		}
	}
	for domain := range desired {
		entry, ok := r.domains[domain]
		if !ok {
			entry = &domainEntry{policies: make(map[string]struct{})}
			r.domains[domain] = entry
			added = append(added, domain)
		}
		entry.policies[policy] = struct{}{}
	}
	r.mu.Unlock()

	for _, domain := range added {
		r.resolve(context.Background(), domain)
	}
}

func (r *domainResolver) addresses(domain string) (v4, v6 []string) {
	normalized, ok := normalizeDomain(domain)
	if !ok {
		return nil, nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	entry, ok := r.domains[normalized]
	if !ok {
		return nil, nil
	}
	return slices.Clone(entry.v4), slices.Clone(entry.v6)
}

func (r *domainResolver) run(ctx context.Context) {
	ticker := time.NewTicker(domainResolveInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.mu.Lock()
			domains := make([]string, 0, len(r.domains))
			for domain := range r.domains {
				domains = append(domains, domain)
			}
			r.mu.Unlock()
			for _, domain := range domains {
				r.resolve(ctx, domain)
			}
		}
	}
}

func (r *domainResolver) resolve(ctx context.Context, domain string) {
	lookupCtx, cancel := context.WithTimeout(ctx, domainResolveTimeout)
	defer cancel()

	addrs, err := r.lookup(lookupCtx, domain)
	if err != nil {
		klog.V(3).Infof("failed to resolve domain %s: %v", domain, err)
		return
	}

	var v4, v6 []string
	for _, addr := range addrs {
		ip := addr.IP.String()
		if util.CheckProtocol(ip) == kubeovnv1.ProtocolIPv4 {
			v4 = append(v4, ip)
		} else {
			v6 = append(v6, ip)
		}
	}
	slices.Sort(v4)
	slices.Sort(v6)

	var policies []string
	r.mu.Lock()
	entry, ok := r.domains[domain]
	if ok && (!slices.Equal(entry.v4, v4) || !slices.Equal(entry.v6, v6)) {
		entry.v4 = v4
		entry.v6 = v6
		for policy := range entry.policies {
			policies = append(policies, policy)
		}
	}
	r.mu.Unlock()

	for _, policy := range policies {
		klog.Infof("resolved addresses of domain %s changed, requeue policy %s", domain, policy)
		r.notify(policy)
	}
}
