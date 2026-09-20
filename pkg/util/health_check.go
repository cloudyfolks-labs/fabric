package util

import (
	"net/http"
	"sync/atomic"

	"k8s.io/klog/v2"
)

type livenessProbe func() error

var livezProbe atomic.Pointer[livenessProbe]

func RegisterLivezProbe(p func() error) {
	if p == nil {
		livezProbe.Store(nil)
		return
	}
	fn := livenessProbe(p)
	livezProbe.Store(&fn)
}

func DefaultHealthCheckHandler(w http.ResponseWriter, _ *http.Request) {
	if _, err := w.Write([]byte("ok")); err != nil {
		klog.Errorf("failed to write health check response: %v", err)
	}
}

func LivezHandler(w http.ResponseWriter, r *http.Request) {
	if p := livezProbe.Load(); p != nil {
		if err := (*p)(); err != nil {
			klog.Warningf("liveness probe failed: %v", err)
			http.Error(w, "probe failed", http.StatusServiceUnavailable)
			return
		}
	}
	DefaultHealthCheckHandler(w, r)
}
