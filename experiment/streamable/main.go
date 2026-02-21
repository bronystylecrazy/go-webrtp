package main

import (
	"bytes"
	"embed"
	"encoding/binary"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/Eyevinn/mp4ff/mp4"
	"github.com/alecthomas/kong"
	"github.com/bluenviron/gortsplib/v5"
	"github.com/bluenviron/gortsplib/v5/pkg/base"
	"github.com/bluenviron/gortsplib/v5/pkg/format"
	"github.com/gorilla/websocket"
	"github.com/pion/rtp"
)

//go:embed index.html
var indexFS embed.FS

type CLI struct {
	RTSP string `help:"RTSP URL to stream from" short:"r" required:""`
	Port int    `help:"HTTP server port" default:"8080" short:"p"`
}

// hub holds all connected WebSocket clients.
// Every client channel has buffer=1. On broadcast we always replace the
// buffered frame with the newest one — so slow clients never lag behind,
// they skip frames instead of queuing them.
type hub struct {
	mu      sync.RWMutex
	clients map[chan []byte]struct{}
	init    []byte
}

func newHub() *hub {
	return &hub{clients: make(map[chan []byte]struct{})}
}

func (h *hub) setInit(data []byte) {
	h.mu.Lock()
	h.init = make([]byte, len(data))
	copy(h.init, data)
	h.mu.Unlock()
}

func (h *hub) getInit() []byte {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.init
}

func (h *hub) subscribe() chan []byte {
	ch := make(chan []byte, 1) // exactly one frame buffer — newest wins
	h.mu.Lock()
	h.clients[ch] = struct{}{}
	h.mu.Unlock()
	return ch
}

func (h *hub) unsubscribe(ch chan []byte) {
	h.mu.Lock()
	delete(h.clients, ch)
	close(ch)
	h.mu.Unlock()
}

// broadcast delivers the latest frame to every client immediately.
// If a client hasn't consumed the previous frame yet, we overwrite it
// with the newest one — clients always receive the latest frame, never stale ones.
func (h *hub) broadcast(data []byte) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for ch := range h.clients {
		select {
		case <-ch: // evict stale frame
		default:
		}
		select {
		case ch <- data: // deliver latest
		default:
		}
	}
}

func buildInitH264(sps, pps []byte) ([]byte, error) {
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

func buildInitH265(vps, sps, pps []byte) ([]byte, error) {
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

func annexBtoAVCC(au [][]byte) []byte {
	var buf bytes.Buffer
	for _, nalu := range au {
		ln := make([]byte, 4)
		binary.BigEndian.PutUint32(ln, uint32(len(nalu)))
		buf.Write(ln)
		buf.Write(nalu)
	}
	return buf.Bytes()
}

func buildFragment(seqNr uint32, dts uint64, dur uint32, isIDR bool, avcc []byte) ([]byte, error) {
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

var wsUpgrader = websocket.Upgrader{
	CheckOrigin:     func(r *http.Request) bool { return true },
	ReadBufferSize:  1024,
	WriteBufferSize: 128 * 1024,
}

func main() {
	cli := &CLI{}
	kctx := kong.Parse(cli)

	h := newHub()

	u, err := base.ParseURL(cli.RTSP)
	if err != nil {
		log.Fatalf("invalid RTSP URL: %v", err)
	}

	c := &gortsplib.Client{
		Scheme: u.Scheme,
		Host:   u.Host,
	}
	if err := c.Start(); err != nil {
		log.Fatalf("RTSP start: %v", err)
	}
	defer c.Close()

	desc, _, err := c.Describe(u)
	if err != nil {
		log.Fatalf("RTSP describe: %v", err)
	}
	log.Printf("found %d media track(s)", len(desc.Medias))

	var (
		seqNr  uint32
		prevTS uint32
		tsOff  uint64
	)

	processAU := func(au [][]byte, ts uint32, isIDR bool) {
		if h.getInit() == nil {
			return
		}
		if prevTS != 0 && ts < prevTS {
			tsOff += 0x100000000
		}
		dts := tsOff + uint64(ts)
		dur := uint32(9000) // fallback 90kHz/10fps
		if prevTS != 0 && ts > prevTS {
			if d := ts - prevTS; d > 0 && d < 90000 {
				dur = d
			}
		}
		prevTS = ts

		avcc := annexBtoAVCC(au)
		seqNr++
		frag, err := buildFragment(seqNr, dts, dur, isIDR, avcc)
		if err != nil {
			log.Printf("buildFragment: %v", err)
			return
		}
		h.broadcast(frag)
	}

	var h264Format *format.H264
	h264Media := desc.FindFormat(&h264Format)

	var h265Format *format.H265
	h265Media := desc.FindFormat(&h265Format)

	switch {
	case h264Media != nil:
		log.Printf("using H264 track")
		rtpDec, err := h264Format.CreateDecoder()
		if err != nil {
			log.Fatalf("H264 decoder: %v", err)
		}
		if err := c.SetupAll(desc.BaseURL, desc.Medias); err != nil {
			log.Fatalf("RTSP setup: %v", err)
		}
		c.OnPacketRTP(h264Media, h264Format, func(pkt *rtp.Packet) {
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
			if h.getInit() == nil {
				sps := h264Format.SPS
				pps := h264Format.PPS
				if len(inSPS) > 0 {
					sps = inSPS
				}
				if len(inPPS) > 0 {
					pps = inPPS
				}
				if len(sps) == 0 || len(pps) == 0 {
					return
				}
				initSeg, err := buildInitH264(sps, pps)
				if err != nil {
					log.Printf("buildInitH264: %v", err)
					return
				}
				h.setInit(initSeg)
				log.Printf("H264 init ready (%d bytes)", len(initSeg))
			}
			processAU(au, pkt.Timestamp, isIDR)
		})

	case h265Media != nil:
		log.Printf("using H265 track")
		rtpDec, err := h265Format.CreateDecoder()
		if err != nil {
			log.Fatalf("H265 decoder: %v", err)
		}
		if err := c.SetupAll(desc.BaseURL, desc.Medias); err != nil {
			log.Fatalf("RTSP setup: %v", err)
		}
		c.OnPacketRTP(h265Media, h265Format, func(pkt *rtp.Packet) {
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
			if h.getInit() == nil {
				vps, sps, pps := h265Format.VPS, h265Format.SPS, h265Format.PPS
				if len(vps) == 0 || len(sps) == 0 || len(pps) == 0 {
					return
				}
				initSeg, err := buildInitH265(vps, sps, pps)
				if err != nil {
					log.Printf("buildInitH265: %v", err)
					return
				}
				h.setInit(initSeg)
				log.Printf("H265 init ready (%d bytes)", len(initSeg))
			}
			processAU(au, pkt.Timestamp, isIDR)
		})

	default:
		log.Fatalf("no H264 or H265 video track found")
	}

	if _, err := c.Play(nil); err != nil {
		log.Fatalf("RTSP play: %v", err)
	}
	log.Printf("RTSP stream active")

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		data, _ := indexFS.ReadFile("index.html")
		w.Header().Set("Content-Type", "text/html")
		w.Write(data)
	})
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		conn, err := wsUpgrader.Upgrade(w, r, nil)
		if err != nil {
			log.Printf("WS upgrade: %v", err)
			return
		}
		defer conn.Close()
		log.Printf("client connected: %s", r.RemoteAddr)

		initData := h.getInit()
		if initData == nil {
			log.Printf("stream not ready, closing %s", r.RemoteAddr)
			return
		}
		conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
		if err := conn.WriteMessage(websocket.BinaryMessage, initData); err != nil {
			return
		}

		ch := h.subscribe()
		defer func() {
			h.unsubscribe(ch)
			log.Printf("client disconnected: %s", r.RemoteAddr)
		}()

		for frag := range ch {
			conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
			if err := conn.WriteMessage(websocket.BinaryMessage, frag); err != nil {
				return
			}
		}
	})

	addr := fmt.Sprintf(":%d", cli.Port)
	srv := &http.Server{Addr: addr, Handler: mux}
	go func() {
		log.Printf("listening on http://localhost%s", addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("HTTP: %v", err)
		}
	}()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	<-sigChan
	log.Printf("shutting down")
	kctx.Exit(0)
}
