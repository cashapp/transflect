package main

import (
	"context"
	"fmt"
	"net/http"

	"github.com/pkg/errors"
	"github.com/rs/zerolog/log"
)

type probesServer struct {
	http.Server
}

func newProbesServer() *probesServer {
	ok := func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "OK")
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/_liveness", ok)
	mux.HandleFunc("/_readiness", ok)
	return &probesServer{
		Server: http.Server{
			Addr:    ":8080",
			Handler: mux,
		},
	}
}

func (s *probesServer) start() error {
	if err := s.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return errors.Wrap(err, "cannot start probes server")
	}
	return nil
}

func (s *probesServer) stop() {
	if err := s.Shutdown(context.Background()); err != nil {
		log.Logger.Error().Err(err).Msg("cannot stop HTTP server")
	}
}
