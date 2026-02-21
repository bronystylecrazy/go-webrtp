package main

import (
	"embed"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/alecthomas/kong"
	"github.com/bluenviron/gohlslib/v2"
	"github.com/bluenviron/gohlslib/v2/pkg/codecs"
	"github.com/bluenviron/gortsplib/v5"
	"github.com/bluenviron/gortsplib/v5/pkg/base"
	"github.com/bluenviron/gortsplib/v5/pkg/format"
	"github.com/pion/rtp"
)

//go:embed index.html
var indexFS embed.FS

// CLI holds command-line configuration.
type CLI struct {
	RTSP string `help:"RTSP URL to stream from" short:"r" required:""`
	Port int    `help:"HTTP server port" default:"8080" short:"p"`
}

func main() {
	cli := &CLI{}
	kctx := kong.Parse(cli)

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

	var hlsMuxer *gohlslib.Muxer
	var videoTrack *gohlslib.Track

	var h264Format *format.H264
	h264Media := desc.FindFormat(&h264Format)

	var h265Format *format.H265
	h265Media := desc.FindFormat(&h265Format)

	switch {
	case h264Media != nil:
		log.Printf("using H264 track (clockRate=%d)", h264Format.ClockRate())

		videoTrack = &gohlslib.Track{
			Codec: &codecs.H264{
				SPS: h264Format.SPS,
				PPS: h264Format.PPS,
			},
			// ClockRate must match the RTP clock rate — always 90000 for H264/H265
			ClockRate: h264Format.ClockRate(),
		}
		hlsMuxer = &gohlslib.Muxer{
			Tracks:             []*gohlslib.Track{videoTrack},
			Variant:            gohlslib.MuxerVariantLowLatency,
			SegmentCount:       7,
			SegmentMinDuration: 1 * time.Second,
		}
		if err := hlsMuxer.Start(); err != nil {
			log.Fatalf("HLS muxer start: %v", err)
		}
		defer hlsMuxer.Close()

		rtpDec, err := h264Format.CreateDecoder()
		if err != nil {
			log.Fatalf("H264 decoder create: %v", err)
		}

		if err := c.SetupAll(desc.BaseURL, desc.Medias); err != nil {
			log.Fatalf("RTSP setup: %v", err)
		}

		c.OnPacketRTP(h264Media, h264Format, func(pkt *rtp.Packet) {
			au, err := rtpDec.Decode(pkt)
			if err != nil || len(au) == 0 {
				return
			}
			// pts is in 90kHz ticks, same unit as pkt.Timestamp
			if err := hlsMuxer.WriteH264(videoTrack, time.Now(), int64(pkt.Timestamp), au); err != nil {
				log.Printf("WriteH264: %v", err)
			}
		})

	case h265Media != nil:
		log.Printf("using H265 track (clockRate=%d)", h265Format.ClockRate())

		videoTrack = &gohlslib.Track{
			Codec: &codecs.H265{
				VPS: h265Format.VPS,
				SPS: h265Format.SPS,
				PPS: h265Format.PPS,
			},
			// ClockRate must match the RTP clock rate — always 90000 for H264/H265
			ClockRate: h265Format.ClockRate(),
		}
		hlsMuxer = &gohlslib.Muxer{
			Tracks:             []*gohlslib.Track{videoTrack},
			Variant:            gohlslib.MuxerVariantLowLatency,
			SegmentCount:       7,
			SegmentMinDuration: 1 * time.Second,
		}
		if err := hlsMuxer.Start(); err != nil {
			log.Fatalf("HLS muxer start: %v", err)
		}
		defer hlsMuxer.Close()

		rtpDec, err := h265Format.CreateDecoder()
		if err != nil {
			log.Fatalf("H265 decoder create: %v", err)
		}

		if err := c.SetupAll(desc.BaseURL, desc.Medias); err != nil {
			log.Fatalf("RTSP setup: %v", err)
		}

		c.OnPacketRTP(h265Media, h265Format, func(pkt *rtp.Packet) {
			au, err := rtpDec.Decode(pkt)
			if err != nil || len(au) == 0 {
				return
			}
			if err := hlsMuxer.WriteH265(videoTrack, time.Now(), int64(pkt.Timestamp), au); err != nil {
				log.Printf("WriteH265: %v", err)
			}
		})

	default:
		log.Fatalf("no H264 or H265 video track found in stream")
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

	// gohlslib handles /index.m3u8, /init.mp4, segment and part files
	mux.HandleFunc("/hls/", func(w http.ResponseWriter, r *http.Request) {
		r.URL.Path = r.URL.Path[len("/hls"):]
		hlsMuxer.Handle(w, r)
	})

	addr := fmt.Sprintf(":%d", cli.Port)
	srv := &http.Server{Addr: addr, Handler: mux}

	go func() {
		log.Printf("HTTP server on http://localhost%s", addr)
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
