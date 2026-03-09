package webrtp

import (
	"context"
	"fmt"
	"time"

	"github.com/gofiber/fiber/v3"
)

func (r *Instance) Start(addr string) error {
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
	for {
		r.hub.Reset()

		ctx, cancel := context.WithCancel(context.Background())
		r.cancel = cancel

		conn, err := r.connectSource(ctx)
		if err != nil {
			r.logger.Printf("source connect failed: %v", err)
			cancel()
			time.Sleep(10 * time.Second)
			continue
		}
		r.conn = conn

		// Wait for connection to drop or frame timeout
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				r.logger.Printf("source connection dropped, reconnecting")
				goto reconnect
			case <-ticker.C:
				if r.hub.ready.Load() && !r.hub.IsReceivingFrames() {
					r.logger.Printf("no frame received for 1s, reconnecting")
					cancel()
					goto reconnect
				}
			}
		}
	reconnect:
	}
}

func (r *Instance) Stop() error {
	if r.cancel != nil {
		r.cancel()
	}
	if r.conn != nil {
		r.conn.Close()
	}
	return nil
}
