// Package webrtp provides RTSP to WebSocket streaming with fMP4 output.
package webrtp

import (
	"context"
	"log"
	"time"
)

type Config struct {
	RTSP            string
	Logger          *log.Logger
	WriteTimeout    time.Duration
	ReadBufferSize  int
	WriteBufferSize int
}

type Instance struct {
	cfg    *Config
	hub    *Hub
	logger *log.Logger
	conn   *rtspConn
	cancel context.CancelFunc
}

func Init(cfg *Config) *Instance {
	logger := cfg.Logger
	if logger == nil {
		logger = log.Default()
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
			RTSP:            cfg.RTSP,
			Logger:          logger,
			WriteTimeout:    writeTimeout,
			ReadBufferSize:  readBuf,
			WriteBufferSize: writeBuf,
		},
		hub:    NewHub(),
		logger: logger,
	}
}

func (r *Instance) Ready() bool {
	return r.hub.GetInit() != nil
}

func (r *Instance) GetHub() *Hub {
	return r.hub
}
