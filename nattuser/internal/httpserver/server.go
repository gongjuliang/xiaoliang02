package httpserver

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"nattuser/internal/config"
	"nattuser/internal/logger"
)

type Server struct {
	server          *http.Server
	shutdownTimeout time.Duration
	log             *logger.Logger
}

func New(cfg config.HTTPConfig, handler http.Handler, log *logger.Logger) *Server {
	return &Server{
		server: &http.Server{
			Addr:         fmt.Sprintf("%s:%d", cfg.Host, cfg.Port),
			Handler:      handler,
			ReadTimeout:  time.Duration(cfg.ReadTimeoutSeconds) * time.Second,
			WriteTimeout: time.Duration(cfg.WriteTimeoutSeconds) * time.Second,
			IdleTimeout:  time.Duration(cfg.IdleTimeoutSeconds) * time.Second,
		},
		shutdownTimeout: time.Duration(cfg.ShutdownTimeoutSeconds) * time.Second,
		log:             log,
	}
}

func (s *Server) Run(ctx context.Context) error {
	errCh := make(chan error, 1)
	go func() {
		if s.log != nil {
			s.log.Infof("http server listening on %s", s.server.Addr)
		}
		err := s.server.ListenAndServe()
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), s.shutdownTimeout)
		defer cancel()
		if s.log != nil {
			s.log.Infof("http server shutting down")
		}
		return s.server.Shutdown(shutdownCtx)
	case err := <-errCh:
		return err
	}
}
