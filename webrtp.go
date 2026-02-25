// Package webrtp provides RTSP to WebSocket streaming with fMP4 output.
package webrtp

import (
	"context"
	"log"
	"time"
)

type Logger interface {
	Print(v ...interface{})
	Printf(format string, v ...interface{})
}

type Config struct {
	Rtsp            string
	Logger          Logger
	WriteTimeout    time.Duration
	ReadBufferSize  int
	WriteBufferSize int
}

type Instance struct {
	cfg    *Config
	hub    *Hub
	logger Logger
	conn   *rtspConn
	cancel context.CancelFunc
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
	return &Instance{
		cfg: &Config{
			Rtsp:            cfg.Rtsp,
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
