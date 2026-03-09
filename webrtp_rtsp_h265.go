package webrtp

import (
	"fmt"

	"github.com/bluenviron/gortsplib/v5"
	"github.com/bluenviron/gortsplib/v5/pkg/description"
	"github.com/bluenviron/gortsplib/v5/pkg/format"
	"github.com/bluenviron/gortsplib/v5/pkg/format/rtph265"
	"github.com/pion/rtp"
)

func (r *Instance) setupH265(c *gortsplib.Client, desc *description.Session, media *description.Media, f *format.H265, h *videoHandler) error {
	r.logger.Printf("using H265 track")
	rtpDec, err := f.CreateDecoder()
	if err != nil {
		return fmt.Errorf("H265 decoder: %w", err)
	}
	if err := c.SetupAll(desc.BaseURL, desc.Medias); err != nil {
		return fmt.Errorf("RTSP setup: %w", err)
	}
	c.OnPacketRTP(media, f, func(pkt *rtp.Packet) {
		r.handleH265Packet(pkt, rtpDec, f, h)
	})
	return nil
}

func (r *Instance) handleH265Packet(pkt *rtp.Packet, rtpDec *rtph265.Decoder, f *format.H265, h *videoHandler) {
	au, err := rtpDec.Decode(pkt)
	if err != nil || len(au) == 0 {
		return
	}
	h.processH265(au, pkt.Timestamp, f.VPS, f.SPS, f.PPS)
}
