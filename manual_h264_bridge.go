package webrtp

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/gofiber/fiber/v3"
)

// H264AccessUnit is a single encoded H264 access unit with its 90kHz timestamp.
type H264AccessUnit struct {
	NALUs  [][]byte
	PTS90k uint32
}

// H264Bridge adapts externally-produced H264 access units into a WebRTP endpoint.
type H264Bridge struct {
	inst *Instance
}

// NewH264Bridge creates a bridge that can publish H264 access units to browser clients.
func NewH264Bridge(cfg *Config) *H264Bridge {
	if cfg == nil {
		cfg = &Config{}
	}
	cfgCopy := *cfg
	if strings.TrimSpace(cfgCopy.StreamName) == "" {
		cfgCopy.StreamName = "preview"
	}
	return &H264Bridge{inst: Init(&cfgCopy)}
}

// Instance exposes the underlying WebRTP instance for advanced control.
func (b *H264Bridge) Instance() *Instance {
	if b == nil {
		return nil
	}
	return b.inst
}

// Handler exposes the bridge as a WebSocket endpoint for browser clients.
func (b *H264Bridge) Handler() fiber.Handler {
	if b == nil || b.inst == nil {
		return func(c fiber.Ctx) error { return fiber.ErrServiceUnavailable }
	}
	return b.inst.Handler()
}

// PublishH264AccessUnit forwards a single H264 access unit into WebRTP.
func (b *H264Bridge) PublishH264AccessUnit(au [][]byte, pts90k uint32) {
	if b == nil || b.inst == nil {
		return
	}
	b.inst.PublishH264AccessUnit(au, pts90k)
}

// Pump forwards access units from a channel until the context is canceled or the channel closes.
func (b *H264Bridge) Pump(ctx context.Context, stream <-chan H264AccessUnit) error {
	if b == nil || b.inst == nil || stream == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case au, ok := <-stream:
			if !ok {
				return nil
			}
			b.inst.PublishH264AccessUnit(au.NALUs, au.PTS90k)
		}
	}
}

// Close releases bridge resources and resets the underlying stream state.
func (b *H264Bridge) Close() error {
	if b == nil || b.inst == nil {
		return nil
	}
	return b.inst.Stop()
}

// H264Fanout duplicates an access-unit stream to multiple consumers.
type H264Fanout struct {
	outputs []chan H264AccessUnit
}

// NewH264Fanout allocates buffered output channels for a single producer stream.
func NewH264Fanout(count, buffer int) *H264Fanout {
	if count < 1 {
		count = 1
	}
	if buffer < 0 {
		buffer = 0
	}
	outputs := make([]chan H264AccessUnit, 0, count)
	for i := 0; i < count; i++ {
		outputs = append(outputs, make(chan H264AccessUnit, buffer))
	}
	return &H264Fanout{outputs: outputs}
}

// Output returns one of the fanout output channels.
func (f *H264Fanout) Output(index int) <-chan H264AccessUnit {
	if f == nil || index < 0 || index >= len(f.outputs) {
		return nil
	}
	return f.outputs[index]
}

// Run copies each access unit to every output until the input closes or the context is canceled.
func (f *H264Fanout) Run(ctx context.Context, input <-chan H264AccessUnit) error {
	if f == nil || input == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	defer f.closeOutputs()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case au, ok := <-input:
			if !ok {
				return nil
			}
			copyAU := cloneH264AccessUnit(au)
			for idx, output := range f.outputs {
				item := copyAU
				if idx+1 < len(f.outputs) {
					item = cloneH264AccessUnit(au)
				}
				select {
				case <-ctx.Done():
					return ctx.Err()
				case output <- item:
				}
			}
		}
	}
}

func (f *H264Fanout) closeOutputs() {
	if f == nil {
		return
	}
	for _, output := range f.outputs {
		close(output)
	}
}

func cloneH264AccessUnit(src H264AccessUnit) H264AccessUnit {
	dst := H264AccessUnit{PTS90k: src.PTS90k}
	if len(src.NALUs) == 0 {
		return dst
	}
	dst.NALUs = make([][]byte, 0, len(src.NALUs))
	for _, nalu := range src.NALUs {
		dst.NALUs = append(dst.NALUs, append([]byte(nil), nalu...))
	}
	return dst
}

// H264KeyframeSampler forwards roughly one IDR access unit per interval from a source stream.
type H264KeyframeSampler struct {
	mu       sync.Mutex
	interval int64
	lastUnix int64
}

// NewH264KeyframeSampler creates a wall-clock sampler for AI-oriented keyframe extraction.
func NewH264KeyframeSampler(intervalSeconds int64) *H264KeyframeSampler {
	if intervalSeconds <= 0 {
		intervalSeconds = 1
	}
	return &H264KeyframeSampler{interval: intervalSeconds}
}

// Accept reports whether the access unit should be forwarded as the next sampled keyframe.
func (s *H264KeyframeSampler) Accept(au H264AccessUnit) bool {
	if !rawH264AccessUnitIsIDR(au.NALUs) {
		return false
	}
	now := time.Now().Unix()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lastUnix != 0 && now-s.lastUnix < s.interval {
		return false
	}
	s.lastUnix = now
	return true
}
