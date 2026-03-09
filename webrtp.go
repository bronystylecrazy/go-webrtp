// Package webrtp provides RTSP to WebSocket streaming with fMP4 output.
package webrtp

import (
	"context"
	"log"
	"strings"
	"sync/atomic"
	"time"
)

type Logger interface {
	Print(v ...interface{})
	Printf(format string, v ...interface{})
}

type Config struct {
	SourceType      string
	Rtsp            string
	Device          string
	Codec           string
	FrameRate       float64
	BitrateKbps     int
	Logger          Logger
	WriteTimeout    time.Duration
	ReadBufferSize  int
	WriteBufferSize int
}

type Instance struct {
	cfg    *Config
	hub    *Hub
	logger Logger
	conn   sourceConn
	cancel context.CancelFunc
	stop   atomic.Bool
}

type stdLogger struct{}

func (s stdLogger) Print(v ...interface{})                 { log.Print(v...) }
func (s stdLogger) Printf(format string, v ...interface{}) { log.Printf(format, v...) }

func Init(cfg *Config) *Instance {
	logger := cfg.Logger
	if logger == nil {
		logger = stdLogger{}
	}
	writeTimeout := cfg.WriteTimeout
	if writeTimeout == 0 {
		writeTimeout = 2 * time.Second
	}
	readBuf := cfg.ReadBufferSize
	if readBuf == 0 {
		readBuf = 1024
	}
	writeBuf := cfg.WriteBufferSize
	if writeBuf == 0 {
		writeBuf = 128 * 1024
	}
	sourceType := strings.ToLower(strings.TrimSpace(cfg.SourceType))
	if sourceType == "" {
		sourceType = "rtsp"
	}
	return &Instance{
		cfg: &Config{
			SourceType:      sourceType,
			Rtsp:            cfg.Rtsp,
			Device:          cfg.Device,
			Codec:           strings.ToLower(strings.TrimSpace(cfg.Codec)),
			FrameRate:       cfg.FrameRate,
			BitrateKbps:     cfg.BitrateKbps,
			Logger:          logger,
			WriteTimeout:    writeTimeout,
			ReadBufferSize:  readBuf,
			WriteBufferSize: writeBuf,
		},
		hub:    NewHub(),
		logger: logger,
	}
}

func (r *Instance) InstanceReady() bool {
	return r.hub.GetInit() != nil
}

func (r *Instance) GetHub() *Hub {
	return r.hub
}
