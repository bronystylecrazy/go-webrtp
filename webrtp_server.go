package webrtp

import (
	"context"
	"fmt"

	"github.com/gofiber/fiber/v3"
)

func (r *Instance) Start(addr string) error {
	ctx, cancel := context.WithCancel(context.Background())
	r.cancel = cancel

	conn, err := r.connectRtsp(ctx)
	if err != nil {
		cancel()
		return fmt.Errorf("rtsp connect: %w", err)
	}
	r.conn = conn

	app := fiber.New()
	app.All("/ws", r.Handler())

	r.logger.Printf("listening on http://localhost%s", addr)
	return app.Listen(addr)
}

func (r *Instance) Connect() error {
	ctx, cancel := context.WithCancel(context.Background())
	r.cancel = cancel

	conn, err := r.connectRtsp(ctx)
	if err != nil {
		cancel()
		return fmt.Errorf("rtsp connect: %w", err)
	}
	r.conn = conn
	return nil
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
