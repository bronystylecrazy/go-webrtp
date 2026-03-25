package usbauto

import (
	"log"

	gwebrtp "github.com/bronystylecrazy/go-webrtp"
)

type logger interface {
	Printf(format string, v ...interface{})
}

type stdLogger struct{}

func (stdLogger) Printf(format string, v ...interface{}) {
	log.Printf(format, v...)
}

type options struct {
	targetFPS          float64
	previewHeight      int
	encoder            string
	encoderArgs        []string
	h264Profile        string
	inputCodec         string
	inputFormat        string
	inputArgs          []string
	bestBitrateKbps    int
	previewBitrateKbps int
	streamBuffer       int
	logger             logger
}

func defaultOptions() options {
	return options{
		targetFPS:     0,
		previewHeight: 720,
		h264Profile:   "high",
		streamBuffer:  32,
		logger:        stdLogger{},
	}
}

// Option customizes how usbauto selects and publishes streams.
type Option func(*options)

// WithTargetFPS sets the preferred source FPS when choosing the best input mode.
func WithTargetFPS(fps float64) Option {
	return func(o *options) {
		if fps > 0 {
			o.targetFPS = fps
		}
	}
}

// WithPreviewHeight sets the scaled preview height. Aspect ratio is preserved automatically.
func WithPreviewHeight(height int) Option {
	return func(o *options) {
		if height > 0 {
			o.previewHeight = height
		}
	}
}

// WithEncoder sets the FFmpeg H264 encoder for both branches.
func WithEncoder(name string, args ...string) Option {
	return func(o *options) {
		if name != "" {
			o.encoder = name
		}
		o.encoderArgs = append([]string(nil), args...)
	}
}

// WithH264Profile sets the output H264 profile for both branches.
func WithH264Profile(profile string) Option {
	return func(o *options) {
		if profile != "" {
			o.h264Profile = profile
		}
	}
}

// WithInputCodec sets the preferred camera-side codec or pixel format before FFmpeg decodes it.
// Examples: "mjpeg", "h264", "yuyv422".
func WithInputCodec(codec string) Option {
	return func(o *options) {
		if codec != "" {
			o.inputCodec = codec
		}
	}
}

// WithInputFormat overrides the inferred FFmpeg input format.
func WithInputFormat(format string) Option {
	return func(o *options) {
		if format != "" {
			o.inputFormat = format
		}
	}
}

// WithInputArgs appends extra arguments before FFmpeg's -i.
func WithInputArgs(args ...string) Option {
	return func(o *options) {
		o.inputArgs = append(o.inputArgs, args...)
	}
}

// WithBitrates sets optional encoder bitrates in Kbps for the best and preview branches.
func WithBitrates(bestKbps, previewKbps int) Option {
	return func(o *options) {
		if bestKbps > 0 {
			o.bestBitrateKbps = bestKbps
		}
		if previewKbps > 0 {
			o.previewBitrateKbps = previewKbps
		}
	}
}

// WithStreamBuffer changes the per-stream access-unit channel depth.
func WithStreamBuffer(size int) Option {
	return func(o *options) {
		if size >= 0 {
			o.streamBuffer = size
		}
	}
}

// WithLogger overrides the package logger.
func WithLogger(l gwebrtp.Logger) Option {
	return func(o *options) {
		if l != nil {
			o.logger = l
		}
	}
}
