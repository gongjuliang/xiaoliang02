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
	"time"

	"nattserver/internal/config"
	"nattserver/internal/logger"
)

// Server HTTP服务器封装，支持HTTP/HTTPS、超时配置和优雅关闭。
type Server struct {
	server          *http.Server   // 底层HTTP服务器实例
	shutdownTimeout time.Duration  // 优雅关闭超时时间
	log             *logger.Logger // 日志记录器
	httpsEnabled    bool           // 是否启用HTTPS
	certFile        string         // HTTPS证书文件路径
	keyFile         string         // HTTPS私钥文件路径
}

// New 创建HTTP服务器封装实例，配置监听地址、超时和TLS参数。
// 参数cfg：HTTP配置（地址/端口/超时/HTTPS）。
// 参数handler：HTTP请求处理器（Gin Engine等）。
// 参数log：日志记录器。
// 返回值：初始化好的Server。
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

// Run 启动HTTP服务器，HTTPS模式下先校验证书，在ctx取消时优雅关闭。
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
			_, port, err := net.SplitHostPort(s.server.Addr)
			if err == nil {
				s.log.Infof("========================================================")
				s.log.Infof("内网穿透-服务端 管理后台网址:%s://127.0.0.1:%s", scheme, port)
				s.log.Infof("========================================================")
			}
			s.log.Infof("内网穿透-服务端 %s 服务 正在监听 %s", scheme, s.server.Addr)

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
			s.log.Infof("http 服务正在关闭")
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
