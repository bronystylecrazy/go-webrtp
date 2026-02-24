package webrtp

import (
	"fmt"

	"github.com/bluenviron/gortsplib/v5"
	"github.com/bluenviron/gortsplib/v5/pkg/description"
	"github.com/bluenviron/gortsplib/v5/pkg/format"
	"github.com/bluenviron/gortsplib/v5/pkg/format/rtph265"
	"github.com/bluenviron/mediacommon/v2/pkg/codecs/h265"
	"github.com/pion/rtp"
)

func (r *Instance) setupH265(c *gortsplib.Client, desc *description.Session, media *description.Media, f *format.H265, h *rtspHandler) error {
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

func (r *Instance) handleH265Packet(pkt *rtp.Packet, rtpDec *rtph265.Decoder, f *format.H265, h *rtspHandler) {
	au, err := rtpDec.Decode(pkt)
	if err != nil || len(au) == 0 {
		return
	}
	isIDR := false
	for _, nalu := range au {
		if len(nalu) == 0 {
			continue
		}
		if t := (nalu[0] >> 1) & 0x3F; t >= 16 && t <= 23 {
			isIDR = true
			break
		}
	}
	if r.hub.GetInit() == nil {
		vps, sps, pps := f.VPS, f.SPS, f.PPS
		if len(vps) == 0 || len(sps) == 0 || len(pps) == 0 {
			return
		}
		initSeg, err := BuildInitH265(vps, sps, pps)
		if err != nil {
			r.logger.Printf("buildInitH265: %v", err)
			return
		}
		r.hub.SetInit(initSeg)

		var width, height int
		var spsInfo h265.SPS
		if err := spsInfo.Unmarshal(sps); err == nil {
			width = spsInfo.Width()
			height = spsInfo.Height()
		}

		r.hub.SetCodecInfo("H265", width, height, 0)
		r.logger.Printf("H265 init ready (%dx%d, %d bytes)", width, height, len(initSeg))
	}
	h.processAU(au, pkt.Timestamp, isIDR)
}
