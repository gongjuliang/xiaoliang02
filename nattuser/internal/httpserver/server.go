// Package httpserver 提供HTTP服务器的封装。
// 支持HTTP和HTTPS两种模式，内置优雅关闭和TLS文件校验。
// 在goroutine中运行监听服务，通过context实现关闭信号传递。
package httpserver

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"time"

	"nattuser/internal/config"
	"nattuser/internal/logger"
)

type Server struct {
	server          *http.Server
	shutdownTimeout time.Duration
	log             *logger.Logger
	httpsEnabled    bool
	certFile        string
	keyFile         string
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
		httpsEnabled:    cfg.HTTPSEnabled,
		certFile:        cfg.CertFile,
		keyFile:         cfg.KeyFile,
	}
}

func (s *Server) Run(ctx context.Context) error {
	if s.httpsEnabled {
		if err := validateTLSFiles(s.certFile, s.keyFile); err != nil {
			return err
		}
	}
	errCh := make(chan error, 1)
	go func() {
		if s.log != nil {
			scheme := "http"
			if s.httpsEnabled {
				scheme = "https"
			}
			s.log.Infof("%s server listening on %s", scheme, s.server.Addr)
		}
		var err error
		if s.httpsEnabled {
			err = s.server.ListenAndServeTLS(s.certFile, s.keyFile)
		} else {
			err = s.server.ListenAndServe()
		}
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

func validateTLSFiles(certFile string, keyFile string) error {
	if _, err := os.Stat(certFile); err != nil {
		return fmt.Errorf("HTTPS证书文件不存在或不可读取: %s", certFile)
	}
	if _, err := os.Stat(keyFile); err != nil {
		return fmt.Errorf("HTTPS私钥文件不存在或不可读取: %s", keyFile)
	}
	return nil
}
