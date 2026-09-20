package util

import (
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fabricv1 "github.com/cloudyfolks-labs/fabric/pkg/apis/fabric/v1"
)

func ReadyServiceCIDRs(sc *networkingv1.ServiceCIDR) []string {
	if sc == nil {
		return nil
	}
	for _, cond := range sc.Status.Conditions {
		if cond.Type == networkingv1.ServiceCIDRConditionReady {
			if cond.Status == metav1.ConditionTrue {
				return sc.Spec.CIDRs
			}
			return nil
		}
	}
	return nil
}

type ServiceCIDRStore struct {
	mu       sync.RWMutex
	fallback []string
	fromAPI  map[string][]string
	handlers []func()
	debounce *time.Timer
	cached   []string

	debounceInterval time.Duration
}

func NewServiceCIDRStore(flagValue string) *ServiceCIDRStore {
	v4, v6 := SplitStringIP(flagValue)
	fallback := make([]string, 0, 2)
	if v4 != "" {
		fallback = append(fallback, v4)
	}
	if v6 != "" {
		fallback = append(fallback, v6)
	}
	s := &ServiceCIDRStore{
		fallback:         fallback,
		fromAPI:          make(map[string][]string),
		debounceInterval: time.Second,
	}
	s.cached = s.merged()
	return s
}

func (s *ServiceCIDRStore) merged() []string {
	seen := make(map[string]struct{}, len(s.fallback)+len(s.fromAPI)*2)
	out := make([]string, 0, len(s.fallback)+len(s.fromAPI)*2)
	add := func(cidr string) {
		cidr = strings.TrimSpace(cidr)
		if cidr == "" {
			return
		}
		if CheckProtocol(cidr) == "" {
			return
		}
		if _, ok := seen[cidr]; ok {
			return
		}
		seen[cidr] = struct{}{}
		out = append(out, cidr)
	}
	for _, cidrs := range s.fromAPI {
		for _, cidr := range cidrs {
			add(cidr)
		}
	}
	if len(out) == 0 {
		for _, cidr := range s.fallback {
			add(cidr)
		}
	}
	sort.Strings(out)
	return out
}

func (s *ServiceCIDRStore) AllCIDRs() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]string, len(s.cached))
	copy(out, s.cached)
	return out
}

func (s *ServiceCIDRStore) V4CIDRs() []string { return s.byProtocol(fabricv1.ProtocolIPv4) }

func (s *ServiceCIDRStore) V6CIDRs() []string { return s.byProtocol(fabricv1.ProtocolIPv6) }

func (s *ServiceCIDRStore) byProtocol(proto string) []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]string, 0, len(s.cached))
	for _, cidr := range s.cached {
		if CheckProtocol(cidr) == proto {
			out = append(out, cidr)
		}
	}
	return out
}

func (s *ServiceCIDRStore) UpsertFromAPI(name string, cidrs []string) bool {
	s.mu.Lock()
	cleaned := make([]string, 0, len(cidrs))
	for _, c := range cidrs {
		c = strings.TrimSpace(c)
		if c != "" {
			cleaned = append(cleaned, c)
		}
	}
	if slices.Equal(s.fromAPI[name], cleaned) {
		s.mu.Unlock()
		return false
	}
	s.fromAPI[name] = cleaned
	changed := s.recomputeLocked()
	s.mu.Unlock()
	if changed {
		s.scheduleFire()
	}
	return changed
}

func (s *ServiceCIDRStore) DeleteFromAPI(name string) bool {
	s.mu.Lock()
	if _, ok := s.fromAPI[name]; !ok {
		s.mu.Unlock()
		return false
	}
	delete(s.fromAPI, name)
	changed := s.recomputeLocked()
	s.mu.Unlock()
	if changed {
		s.scheduleFire()
	}
	return changed
}

func (s *ServiceCIDRStore) recomputeLocked() bool {
	next := s.merged()
	if slices.Equal(s.cached, next) {
		return false
	}
	s.cached = next
	return true
}

func (s *ServiceCIDRStore) OnChange(h func()) {
	s.mu.Lock()
	s.handlers = append(s.handlers, h)
	s.mu.Unlock()
}

func (s *ServiceCIDRStore) scheduleFire() {
	s.mu.Lock()
	if s.debounce != nil {
		s.debounce.Stop()
	}
	s.debounce = time.AfterFunc(s.debounceInterval, s.fireOnChange)
	s.mu.Unlock()
}

func (s *ServiceCIDRStore) fireOnChange() {
	s.mu.RLock()
	handlers := make([]func(), len(s.handlers))
	copy(handlers, s.handlers)
	s.mu.RUnlock()
	for _, h := range handlers {
		go h()
	}
}
