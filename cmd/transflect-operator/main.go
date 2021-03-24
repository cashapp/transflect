package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/alecthomas/kong"
	"github.com/cashapp/transflect/pkg/transflect"
	"github.com/pkg/errors"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/rs/zerolog/pkgerrors"
	"golang.org/x/sync/errgroup"
)

var version = "v0.0.0"

type config struct {
	UseIngress bool             `short:"i" help:"Create and use temporary ingress to access temporary service, e.g. from outside cluster"`
	Address    string           `short:"a" help:"gRPC reflection server address, host:port"`
	Plaintext  bool             `short:"p" help:"Use plain-text; no TLS"`
	LogFormat  string           `help:"Log format ('json', 'std')" enum:"json,std" default:"std"`
	Version    kong.VersionFlag `short:"v" help:"Print version information"`
}

func main() {
	cfg := &config{}
	_ = kong.Parse(cfg, kong.Vars{"version": version})

	if err := run(cfg); err != nil {
		log.Error().Stack().Err(err).Msg("exiting")
		os.Exit(1)
	}
}

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

var errSignal = fmt.Errorf("received shutdown signal")

func run(cfg *config) error {
	setupLogging(cfg)

	// create servers
	probes := newProbesServer()
	operator, err := newOperator(cfg)
	if err != nil {
		return err
	}

	// start servers
	g := &errgroup.Group{}
	g.Go(probes.start)
	g.Go(operator.start)
	g.Go(handleSignals)

	// shutdown
	err = g.Wait()
	if err != nil && !errors.Is(err, errSignal) {
		log.Error().Err(err).Msg("Error causing shutdown")
	}
	probes.stop()
	operator.stop()
	log.Debug().Msg("Shutdown done")

	return nil
}

func handleSignals() error {
	// handle graceful shutdown
	c := make(chan os.Signal, 1)
	signal.Notify(c, syscall.SIGINT, syscall.SIGTERM) // Use SIGQUIT (ctrl+\) for instant shutdown
	sig := <-c
	log.Debug().Str("signal", sig.String()).Msg("Received signal, shutting down")
	return errSignal
}

func setupLogging(cfg *config) {
	log.Logger = log.Output(os.Stdout)
	if cfg.LogFormat == "std" {
		log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stdout})
	}
	zerolog.ErrorStackMarshaler = pkgerrors.MarshalStack
	transflect.LogAttempt = logAttempt
}

type ctxKey string

const ctxReplicaKey = ctxKey("replica")

func logAttempt(ctx context.Context, attempt int, err error) {
	replica := "unknown"
	if v := ctx.Value(ctxReplicaKey); v != nil {
		if s, ok := v.(string); ok {
			replica = s
		}
	}
	log.Error().Int("attempt", attempt).Err(err).Str("replica", replica).Msg("Reflection call error")
}
