package main

import (
	"fmt"
	"log/slog"
	"net/http"
	"os"

	"github.com/infinite-iroha/touka"
)

// SlogAdapter 将 slog.Logger 适配到 touka.Logger 接口
type SlogAdapter struct {
	logger *slog.Logger
}

func NewSlogAdapter(handler slog.Handler) *SlogAdapter {
	return &SlogAdapter{
		logger: slog.New(handler),
	}
}

func (s *SlogAdapter) Debugf(format string, args ...any) {
	s.logger.Debug(fmt.Sprintf(format, args...))
}

func (s *SlogAdapter) Infof(format string, args ...any) {
	s.logger.Info(fmt.Sprintf(format, args...))
}

func (s *SlogAdapter) Warnf(format string, args ...any) {
	s.logger.Warn(fmt.Sprintf(format, args...))
}

func (s *SlogAdapter) Errorf(format string, args ...any) {
	s.logger.Error(fmt.Sprintf(format, args...))
}

func (s *SlogAdapter) Fatalf(format string, args ...any) {
	s.logger.Error(fmt.Sprintf(format, args...))
	os.Exit(1)
}

func (s *SlogAdapter) Panicf(format string, args ...any) {
	s.logger.Error(fmt.Sprintf(format, args...))
	panic(fmt.Sprintf(format, args...))
}

func main() {
	engine := touka.New()

	// 使用 slog 替换默认的 reco.Logger
	handler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	})
	slogAdapter := NewSlogAdapter(handler)
	engine.SetLogger(slogAdapter)

	engine.GET("/", func(c *touka.Context) {
		c.Infof("request received: %s", c.Request.URL.Path)
		c.JSON(http.StatusOK, map[string]string{"message": "hello"})
	})

	// 也可以获取 Logger 接口
	logger := engine.GetLogger()
	logger.Debugf("engine started")

	// 也可以直接使用 slog
	slog.Info("Server running", "addr", ":8080")
	// engine.Run(":8080")
}
