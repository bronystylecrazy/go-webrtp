package webrtp

import (
	"fmt"

	"github.com/bluenviron/gortsplib/v5"
	"github.com/bluenviron/gortsplib/v5/pkg/description"
	"github.com/bluenviron/gortsplib/v5/pkg/format"
	"github.com/bluenviron/gortsplib/v5/pkg/format/rtph264"
	"github.com/bluenviron/mediacommon/v2/pkg/codecs/h264"
	"github.com/pion/rtp"
)

func (r *Instance) setupH264(c *gortsplib.Client, desc *description.Session, media *description.Media, f *format.H264, h *rtspHandler) error {
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

func (r *Instance) handleH264Packet(pkt *rtp.Packet, rtpDec *rtph264.Decoder, f *format.H264, h *rtspHandler) {
	au, err := rtpDec.Decode(pkt)
	if err != nil || len(au) == 0 {
		return
	}
	isIDR := false
	var inSPS, inPPS []byte
	for _, nalu := range au {
		if len(nalu) == 0 {
			continue
		}
		switch nalu[0] & 0x1F {
		case 5:
			isIDR = true
		case 7:
			inSPS = nalu
		case 8:
			inPPS = nalu
		}
	}
	if r.hub.GetInit() == nil {
		sps := f.SPS
		pps := f.PPS
		if len(inSPS) > 0 {
			sps = inSPS
		}
		if len(inPPS) > 0 {
			pps = inPPS
		}
		if len(sps) == 0 || len(pps) == 0 {
			return
		}
		initSeg, err := BuildInitH264(sps, pps)
		if err != nil {
			r.logger.Printf("buildInitH264: %v", err)
			return
		}
		r.hub.SetInit(initSeg)

		var width, height int
		var spsInfo h264.SPS
		if err := spsInfo.Unmarshal(sps); err == nil {
			width = spsInfo.Width()
			height = spsInfo.Height()
		}

		r.hub.SetCodecInfo("H264", width, height, 0)
		r.logger.Printf("H264 init ready (%dx%d, %d bytes)", width, height, len(initSeg))
	}
	h.processAU(au, pkt.Timestamp, isIDR)
}
