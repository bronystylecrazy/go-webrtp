package webrtp

import (
	"context"
	"fmt"

	"github.com/bluenviron/gortsplib/v5"
)

type rtspConn struct {
	client *gortsplib.Client
	cancel context.CancelFunc
}

func (r *rtspConn) Close() {
	if r.cancel != nil {
		r.cancel()
	}
	if r.client != nil {
		r.client.Close()
	}
}

func (r *Instance) connectRtsp(ctx context.Context) (*rtspConn, error) {
	u, err := parseRtspUrl(r.cfg.Rtsp)
	if err != nil {
		return nil, err
	}
	rtspCtx, cancel := context.WithCancel(ctx)
	c := &gortsplib.Client{Scheme: u.Scheme, Host: u.Host}
	if err := c.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("RTSP start: %w", err)
	}
	desc, _, err := c.Describe(u)
	if err != nil {
		c.Close()
		cancel()
		return nil, fmt.Errorf("RTSP describe: %w", err)
	}
	r.logger.Printf("found %d media track(s)", len(desc.Medias))
	h := &videoHandler{hub: r.hub, logger: r.logger}
	if err := r.setupCodecHandler(c, desc, h); err != nil {
		c.Close()
		cancel()
		return nil, err
	}
	if _, err := c.Play(nil); err != nil {
		c.Close()
		cancel()
		return nil, fmt.Errorf("RTSP play: %w", err)
	}
	r.logger.Printf("RTSP stream active")
	go func() {
		<-rtspCtx.Done()
		c.Close()
	}()
	return &rtspConn{client: c, cancel: cancel}, nil
}
