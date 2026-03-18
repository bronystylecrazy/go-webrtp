package webrtp

import (
	"fmt"

	"github.com/bluenviron/gortsplib/v5"
	"github.com/bluenviron/gortsplib/v5/pkg/description"
	"github.com/bluenviron/gortsplib/v5/pkg/format"
	"github.com/bluenviron/gortsplib/v5/pkg/format/rtph264"
	"github.com/pion/rtp"
)

func (r *Instance) setupH264(c *gortsplib.Client, desc *description.Session, media *description.Media, f *format.H264, h *videoHandler) error {
	r.logger.Printf("using H264 track")
	rtpDec, err := f.CreateDecoder()
	if err != nil {
		return fmt.Errorf("H264 decoder: %w", err)
	}
	if err := c.SetupAll(desc.BaseURL, desc.Medias); err != nil {
		return fmt.Errorf("RTSP setup: %w", err)
	}
	c.OnPacketRTP(media, f, func(pkt *rtp.Packet) {
		r.handleH264Packet(pkt, rtpDec, f, h)
	})
	return nil
}

func (r *Instance) handleH264Packet(pkt *rtp.Packet, rtpDec *rtph264.Decoder, f *format.H264, h *videoHandler) {
	au, err := rtpDec.Decode(pkt)
	if err != nil || len(au) == 0 {
		return
	}
	h.processH264(au, pkt.Timestamp, f.SPS, f.PPS)
}
