package serve

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"k8s.io/klog/v2"
)

type Readiness struct {
	ready atomic.Bool
}

func NewReadiness() *Readiness { return &Readiness{} }

func (r *Readiness) SetReady(ready bool) { r.ready.Store(ready) }

func (r *Readiness) Ready() bool { return r.ready.Load() }

type Servers struct {
	metrics *http.Server
	probes  *http.Server
}

func New(metricsAddress, probeAddress string, readiness *Readiness) *Servers {
	s := &Servers{}

	if metricsAddress != "" {
		mux := http.NewServeMux()
		mux.Handle("/metrics", promhttp.Handler())
		s.metrics = &http.Server{Addr: metricsAddress, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	}

	if probeAddress != "" {
		mux := http.NewServeMux()
		mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("ok"))
		})
		mux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) {
			if !readiness.Ready() {
				http.Error(w, "caches not synced", http.StatusServiceUnavailable)
				return
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("ok"))
		})
		s.probes = &http.Server{Addr: probeAddress, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	}

	return s
}

func (s *Servers) Run(ctx context.Context) error {
	logger := klog.FromContext(ctx)

	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for name, server := range map[string]*http.Server{"metrics": s.metrics, "probes": s.probes} {
		if server == nil {
			continue
		}
		logger.Info("serving", "endpoint", name, "address", server.Addr)
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				errs <- err
			}
		}()
	}

	<-ctx.Done()

	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	for _, server := range []*http.Server{s.metrics, s.probes} {
		if server != nil {
			_ = server.Shutdown(shutdownCtx)
		}
	}
	wg.Wait()
	close(errs)
	return <-errs
}
