package main

import (
	"context"
	"fmt"
	"net/http"

	"github.com/pkg/errors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/rs/zerolog/log"
)

type httpServer struct {
	http.Server
	name string
}

func newProbesServer(addr string) *httpServer {
	ok := func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "OK")
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/_liveness", ok)
	mux.HandleFunc("/_readiness", ok)
	return &httpServer{
		Server: http.Server{
			Addr:    addr,
			Handler: mux,
		},
		name: "probes",
	}
}

func newMetricsServer(addr string) *httpServer {
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())
	return &httpServer{
		Server: http.Server{
			Addr:    addr,
			Handler: mux,
		},
		name: "metrics",
	}
}

func (s *httpServer) start(ctx context.Context) error {
	go s.stopOnCancel(ctx)
	if err := s.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return errors.Wrapf(err, "cannot start %s server", s.name)
	}
	return nil
}

func (s *httpServer) stopOnCancel(ctx context.Context) {
	<-ctx.Done()
	if err := s.Shutdown(context.Background()); err != nil {
		log.Logger.Error().Err(err).Str("server", s.name).Msg("cannot stop server")
	}
}
