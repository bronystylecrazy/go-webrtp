package webrtp

import (
	"bytes"
	"encoding/binary"
	"fmt"

	"github.com/Eyevinn/mp4ff/mp4"
	"github.com/bluenviron/gortsplib/v5/pkg/base"
)

// parseRtspUrl validates and parses the RTSP URL.
func parseRtspUrl(rtspURL string) (*base.URL, error) {
	u, err := base.ParseURL(rtspURL)
	if err != nil {
		return nil, fmt.Errorf("invalid RTSP URL: %w", err)
	}
	return u, nil
}

// BuildInitH264 creates an fMP4 init segment for H264 video.
func BuildInitH264(sps, pps []byte) ([]byte, error) {
	init := mp4.CreateEmptyInit()
	trak := init.AddEmptyTrack(90000, "video", "und")
	if err := trak.SetAVCDescriptor("avc1", [][]byte{sps}, [][]byte{pps}, true); err != nil {
		return nil, fmt.Errorf("SetAVCDescriptor: %w", err)
	}
	var buf bytes.Buffer
	if err := init.Encode(&buf); err != nil {
		return nil, fmt.Errorf("encode init: %w", err)
	}
	return buf.Bytes(), nil
}

// BuildInitH265 creates an fMP4 init segment for H265 video.
func BuildInitH265(vps, sps, pps []byte) ([]byte, error) {
	init := mp4.CreateEmptyInit()
	trak := init.AddEmptyTrack(90000, "video", "und")
	if err := trak.SetHEVCDescriptor("hvc1", [][]byte{vps}, [][]byte{sps}, [][]byte{pps}, nil, true); err != nil {
		return nil, fmt.Errorf("SetHEVCDescriptor: %w", err)
	}
	var buf bytes.Buffer
	if err := init.Encode(&buf); err != nil {
		return nil, fmt.Errorf("encode init: %w", err)
	}
	return buf.Bytes(), nil
}

// AnnexbToAvcc converts Annex-B NAL units to AVCC format.
func AnnexbToAvcc(au [][]byte) []byte {
	var buf bytes.Buffer
	for _, nalu := range au {
		ln := make([]byte, 4)
		binary.BigEndian.PutUint32(ln, uint32(len(nalu)))
		buf.Write(ln)
		buf.Write(nalu)
	}
	return buf.Bytes()
}

// AnnexbToNalus splits an Annex-B byte stream into NAL units.
func AnnexbToNalus(data []byte) [][]byte {
	indices := make([]int, 0)
	i := 0
	for i < len(data)-3 {
		if data[i] == 0 && data[i+1] == 0 {
			if data[i+2] == 1 {
				indices = append(indices, i)
				i += 3
				continue
			}
			if i+3 < len(data) && data[i+2] == 0 && data[i+3] == 1 {
				indices = append(indices, i)
				i += 4
				continue
			}
		}
		i++
	}
	if len(indices) == 0 {
		return nil
	}

	nalus := make([][]byte, 0, len(indices))
	for idx, start := range indices {
		offset := 3
		if start+3 < len(data) && data[start+2] == 0 && data[start+3] == 1 {
			offset = 4
		}
		end := len(data)
		if idx+1 < len(indices) {
			end = indices[idx+1]
		}
		nalu := bytes.Trim(data[start+offset:end], "\x00")
		if len(nalu) > 0 {
			nalus = append(nalus, nalu)
		}
	}
	return nalus
}

// BuildFragment creates an fMP4 media fragment.
func BuildFragment(seqNr uint32, dts uint64, dur uint32, isIDR bool, avcc []byte) ([]byte, error) {
	seg := mp4.NewMediaSegment()
	frag, err := mp4.CreateFragment(seqNr, mp4.DefaultTrakID)
	if err != nil {
		return nil, fmt.Errorf("CreateFragment: %w", err)
	}
	seg.AddFragment(frag)
	flags := mp4.NonSyncSampleFlags
	if isIDR {
		flags = mp4.SyncSampleFlags
	}
	frag.AddFullSample(mp4.FullSample{
		Sample: mp4.Sample{
			Flags:                 flags,
			Dur:                   dur,
			Size:                  uint32(len(avcc)),
			CompositionTimeOffset: 0,
		},
		DecodeTime: dts,
		Data:       avcc,
	})
	var buf bytes.Buffer
	if err := seg.Encode(&buf); err != nil {
		return nil, fmt.Errorf("encode segment: %w", err)
	}
	return buf.Bytes(), nil
}
