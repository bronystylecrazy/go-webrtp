package webrtp

import (
	"context"
	"encoding/binary"
	"fmt"
	"sync"

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

type rtspHandler struct {
	hub    *Hub
	logger Logger
	seqNr  uint32
	prevTS uint32
	tsOff  uint64
	mu     sync.Mutex
}

func (r *rtspHandler) processAu(au [][]byte, ts uint32, isIDR bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.hub.GetInit() == nil {
		return
	}
	if r.prevTS != 0 && ts < r.prevTS {
		r.tsOff += 0x100000000
	}
	dts := r.tsOff + uint64(ts)
	dur := uint32(9000)
	if r.prevTS != 0 && ts > r.prevTS {
		if d := ts - r.prevTS; d > 0 && d < 90000 {
			dur = d
			framerate := float64(90000) / float64(dur)
			if framerate > 0 {
				r.hub.SetFramerate(framerate)
			}
		}
	}
	r.prevTS = ts

	// Convert all NAL units to AVCC format (4-byte big-endian length prefix)
	var avccData []byte
	for _, nalu := range au {
		ln := make([]byte, 4)
		binary.BigEndian.PutUint32(ln, uint32(len(nalu)))
		avccData = append(avccData, ln...)
		avccData = append(avccData, nalu...)
	}
	avcc := avccData

	r.seqNr++

	frag, err := BuildFragment(r.seqNr, dts, dur, isIDR, avcc)
	if err != nil {
		r.logger.Printf("buildFragment: %v", err)
		return
	}
	r.hub.Broadcast(frag)
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
	h := &rtspHandler{hub: r.hub, logger: r.logger}
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
