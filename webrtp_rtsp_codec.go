package webrtp

import (
	"fmt"

	"github.com/bluenviron/gortsplib/v5"
	"github.com/bluenviron/gortsplib/v5/pkg/base"
	"github.com/bluenviron/gortsplib/v5/pkg/description"
	"github.com/bluenviron/gortsplib/v5/pkg/format"
)

func parseRTSPURL(rtspURL string) (*base.URL, error) {
	u, err := base.ParseURL(rtspURL)
	if err != nil {
		return nil, fmt.Errorf("invalid RTSP URL: %w", err)
	}
	return u, nil
}

func (r *Instance) setupCodecHandler(c *gortsplib.Client, desc *description.Session, h *rtspHandler) error {
	var h264Format *format.H264
	h264Media := desc.FindFormat(&h264Format)

	var h265Format *format.H265
	h265Media := desc.FindFormat(&h265Format)

	switch {
	case h264Media != nil:
		return r.setupH264(c, desc, h264Media, h264Format, h)
	case h265Media != nil:
		return r.setupH265(c, desc, h265Media, h265Format, h)
	default:
		return fmt.Errorf("no H264 or H265 video track found")
	}
}
