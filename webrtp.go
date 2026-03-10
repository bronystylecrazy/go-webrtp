// Package webrtp provides RTSP to WebSocket streaming with fMP4 output.
package webrtp

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
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
	Path            string
	Codec           string
	Width           int
	Height          int
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

	recorderMu sync.Mutex
	recorder   *Recorder
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
			Path:            cfg.Path,
			Codec:           strings.ToLower(strings.TrimSpace(cfg.Codec)),
			Width:           cfg.Width,
			Height:          cfg.Height,
			FrameRate:       cfg.FrameRate,
			BitrateKbps:     cfg.BitrateKbps,
			Logger:          logger,
			WriteTimeout:    writeTimeout,
			ReadBufferSize:  readBuf,
			WriteBufferSize: writeBuf,
		},
		hub:      NewHub(),
		logger:   logger,
		recorder: NewRecorder(logger),
	}
}

func (r *Instance) InstanceReady() bool {
	return r.hub.GetInit() != nil
}

func (r *Instance) GetHub() *Hub {
	return r.hub
}

func (r *Instance) StartRecording(path, mode, offlineMode string) error {
	r.recorderMu.Lock()
	defer r.recorderMu.Unlock()

	explicitMode := strings.TrimSpace(mode) != ""
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode == "" {
		if requester, ok := r.conn.(interface{ ForceNextKeyFrame() error }); ok && requester != nil {
			mode = "exact"
		} else {
			mode = "instant"
		}
	}
	if r.recorder == nil {
		r.recorder = NewRecorder(r.logger)
	}
	startWithMode := func(selectedMode string) error {
		if err := r.recorder.Start(path, selectedMode, offlineMode); err != nil {
			return err
		}
		if initData := r.hub.GetInit(); initData != nil {
			r.recorder.SetInit(initData)
		}
		return nil
	}
	if err := startWithMode(mode); err != nil {
		return err
	}
	if mode == "exact" {
		requester, ok := r.conn.(interface{ ForceNextKeyFrame() error })
		if !ok {
			_ = r.recorder.Stop()
			if explicitMode {
				return fmt.Errorf("recording mode exact is only supported for sources that can force a keyframe")
			}
			return startWithMode("instant")
		}
		if err := requester.ForceNextKeyFrame(); err != nil {
			_ = r.recorder.Stop()
			if explicitMode {
				return fmt.Errorf("recording mode exact: %w", err)
			}
			r.logger.Printf("recording exact unavailable, falling back to instant: %v", err)
			return startWithMode("instant")
		}
	}
	return nil
}

func (r *Instance) StopRecording() error {
	r.recorderMu.Lock()
	defer r.recorderMu.Unlock()
	if r.recorder == nil {
		return nil
	}
	return r.recorder.Stop()
}

func (r *Instance) RecordingStatus() RecordingStatus {
	r.recorderMu.Lock()
	defer r.recorderMu.Unlock()
	if r.recorder == nil {
		return RecordingStatus{}
	}
	return r.recorder.Status()
}

func (r *Instance) currentRecorder() *Recorder {
	r.recorderMu.Lock()
	defer r.recorderMu.Unlock()
	return r.recorder
}
