package webrtp

import (
	"context"
	"fmt"

	"github.com/gofiber/fiber/v3"
)

func (r *Instance) Start(addr string) error {
	r.stop.Store(false)
	r.ensureKeyframeSink()
	ctx, cancel := context.WithCancel(context.Background())
	r.cancel = cancel

	conn, err := r.connectSource(ctx)
	if err != nil {
		cancel()
		return fmt.Errorf("source connect: %w", err)
	}
	r.conn = conn

	app := fiber.New()
	app.All("/ws", r.Handler())

	r.logger.Printf("listening on http://localhost%s", addr)
	return app.Listen(addr)
}

func (r *Instance) Connect() error {
	return r.runReconnectLoop()
}

func (r *Instance) Stop() error {
	r.stop.Store(true)
	if r.cancel != nil {
		r.cancel()
		r.cancel = nil
	}
	if r.conn != nil {
		r.conn.Close()
		r.conn = nil
	}
	r.hub.Reset()
	if recorder := r.currentRecorder(); recorder != nil {
		_ = recorder.Stop()
	}
	if r.keyframes != nil {
		r.keyframes.Close()
		r.keyframes = nil
	}
	r.publishMu.Lock()
	r.publisher = nil
	r.publishMu.Unlock()
	return nil
}
