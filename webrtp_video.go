package webrtp

import (
	"sync"

	"github.com/bluenviron/mediacommon/v2/pkg/codecs/h264"
	"github.com/bluenviron/mediacommon/v2/pkg/codecs/h265"
)

type videoHandler struct {
	hub    *Hub
	logger Logger
	seqNr  uint32
	prevTS uint32
	tsOff  uint64
	mu     sync.Mutex
}

func (r *videoHandler) processAu(au [][]byte, ts uint32, isIDR bool) {
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

	avcc := AnnexbToAvcc(au)
	r.seqNr++

	frag, err := BuildFragment(r.seqNr, dts, dur, isIDR, avcc)
	if err != nil {
		r.logger.Printf("buildFragment: %v", err)
		return
	}
	r.hub.Broadcast(frag)
}

func (r *videoHandler) processH264(au [][]byte, ts uint32, spsBase, ppsBase []byte) {
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
		sps := spsBase
		pps := ppsBase
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

		r.hub.SetInfo("H264", width, height, 0)
		r.logger.Printf("H264 init ready (%dx%d, %d bytes)", width, height, len(initSeg))
	}
	r.processAu(au, ts, isIDR)
}

func (r *videoHandler) processH265(au [][]byte, ts uint32, vpsBase, spsBase, ppsBase []byte) {
	isIDR := false
	var inVPS, inSPS, inPPS []byte
	for _, nalu := range au {
		if len(nalu) == 0 {
			continue
		}
		switch (nalu[0] >> 1) & 0x3F {
		case 32:
			inVPS = nalu
		case 33:
			inSPS = nalu
		case 34:
			inPPS = nalu
		}
		if t := (nalu[0] >> 1) & 0x3F; t >= 16 && t <= 23 {
			isIDR = true
		}
	}
	if r.hub.GetInit() == nil {
		vps := vpsBase
		sps := spsBase
		pps := ppsBase
		if len(inVPS) > 0 {
			vps = inVPS
		}
		if len(inSPS) > 0 {
			sps = inSPS
		}
		if len(inPPS) > 0 {
			pps = inPPS
		}
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

		r.hub.SetInfo("H265", width, height, 0)
		r.logger.Printf("H265 init ready (%dx%d, %d bytes)", width, height, len(initSeg))
	}
	r.processAu(au, ts, isIDR)
}
