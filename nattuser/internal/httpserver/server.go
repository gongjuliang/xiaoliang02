// Package httpserver 提供HTTP服务器的封装。
// 支持HTTP和HTTPS两种模式，内置优雅关闭和TLS文件校验。
// 在goroutine中运行监听服务，通过context实现关闭信号传递。
package httpserver

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
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
	ipv6Host        string
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
		ipv6Host:        cfg.IPv6Host,
	}
}

func (s *Server) Run(ctx context.Context) error {
	if s.httpsEnabled {
		if err := validateTLSFiles(s.certFile, s.keyFile); err != nil {
			return err
		}
	}
	host, portText, err := net.SplitHostPort(s.server.Addr)
	if err != nil {
		return err
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		return err
	}
	listeners, ipv6Err, err := listenTCPDual(host, s.ipv6Host, port)
	if err != nil {
		return fmt.Errorf("Web IPv4监听失败: %w", err)
	}
	defer closeListeners(listeners)
	if ipv6Err != nil && s.log != nil {
		s.log.Errorf("IPv6 Web监听不可用，已继续使用IPv4: %v", ipv6Err)
	}
	errCh := make(chan error, len(listeners))
	go func() {
		if s.log != nil {
			scheme := "http"
			if s.httpsEnabled {
				scheme = "https"
			}
			_, port, err := net.SplitHostPort(s.server.Addr)
			if err == nil {
				s.log.Infof("============================================================")
				s.log.Infof("小良内网穿透-客户端 管理后台网址:%s://127.0.0.1:%s", scheme, port)
				s.log.Infof("============================================================")
			}
			s.log.Infof("小良内网穿透-客户端 %s 服务 正在监听 %s", scheme, s.server.Addr)

		}
		var err error
		if s.httpsEnabled {
			err = s.server.ServeTLS(listeners[0], s.certFile, s.keyFile)
		} else {
			err = s.server.Serve(listeners[0])
		}
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
			return
		}
		errCh <- nil
	}()
	for _, listener := range listeners[1:] {
		go func(listener net.Listener) {
			var serveErr error
			if s.httpsEnabled {
				serveErr = s.server.ServeTLS(listener, s.certFile, s.keyFile)
			} else {
				serveErr = s.server.Serve(listener)
			}
			if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
				errCh <- serveErr
				return
			}
			errCh <- nil
		}(listener)
	}

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), s.shutdownTimeout)
		defer cancel()
		if s.log != nil {
			s.log.Infof("http 服务已关闭")
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
