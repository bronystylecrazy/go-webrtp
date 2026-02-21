package main

import (
	"bufio"
	"embed"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/alecthomas/kong"
	"github.com/bluenviron/gortsplib/v5"
	"github.com/bluenviron/gortsplib/v5/pkg/base"
	"github.com/bluenviron/gortsplib/v5/pkg/description"
	"github.com/bluenviron/gortsplib/v5/pkg/format"
	"github.com/gofiber/fiber/v3"
	"github.com/pion/rtp"
)

// * embed index.html
//
//go:embed index.html
var indexFS embed.FS

// * cli configuration
type CLI struct {
	RTSP string `help:"RTSP URL to stream from" default:"" short:"r"`
	Port int    `help:"HTTP server port" default:"8080" short:"p"`
}

// * global server state
type Server struct {
	// * RTSP client
	client *gortsplib.Client
	// * mutex for concurrent access
	mu sync.Mutex
	// * RTP packets for streaming
	packets [][]byte
	// * stream active
	active bool
}

// * create new server instance
func ServerNew() *Server {
	return &Server{
		packets: make([][]byte, 0, 1000),
	}
}

// * start RTSP connection
func (s *Server) RTSPStart(rtspURL string) error {
	if rtspURL == "" {
		return fmt.Errorf("RTSP URL is required")
	}

	// * parse RTSP URL
	u, err := base.ParseURL(rtspURL)
	if err != nil {
		return fmt.Errorf("RTSP URL parse failed: %w", err)
	}

	// * create RTSP client
	s.client = &gortsplib.Client{
		Scheme: u.Scheme,
		Host:   u.Host,
	}

	// * start client
	log.Printf("Connecting to RTSP: %s", rtspURL)
	err = s.client.Start()
	if err != nil {
		return fmt.Errorf("RTSP start failed: %w", err)
	}

	// * send DESCRIBE
	log.Printf("Describing stream...")
	desc, _, err := s.client.Describe(u)
	if err != nil {
		s.client.Close()
		return fmt.Errorf("RTSP describe failed: %w", err)
	}

	log.Printf("Found %d media track(s)", len(desc.Medias))

	// * find first video track
	var videoMedia *description.Media
	var videoFormat format.Format
	for _, media := range desc.Medias {
		for _, forma := range media.Formats {
			if _, ok := forma.(*format.H264); ok {
				videoMedia = media
				videoFormat = forma
				log.Printf("Found H264 video track")
				break
			}
			if _, ok := forma.(*format.H265); ok {
				videoMedia = media
				videoFormat = forma
				log.Printf("Found H265 video track")
				break
			}
		}
		if videoMedia != nil {
			break
		}
	}

	if videoMedia == nil {
		s.client.Close()
		return fmt.Errorf("no video track found")
	}

	// * setup all media tracks
	log.Printf("Setting up media tracks...")
	err = s.client.SetupAll(u, desc.Medias)
	if err != nil {
		s.client.Close()
		return fmt.Errorf("RTSP setup failed: %w", err)
	}

	// * set RTP packet callback
	s.client.OnPacketRTP(videoMedia, videoFormat, func(pkt *rtp.Packet) {
		// * serialize packet
		data, err := pkt.Marshal()
		if err != nil {
			log.Printf("RTP marshal error: %v", err)
			return
		}

		// * store packet
		s.mu.Lock()
		if len(s.packets) < 1000 {
			s.packets = append(s.packets, data)
		} else {
			// * drop oldest packet (ring buffer)
			s.packets = append(s.packets[1:], data)
		}
		s.active = true
		s.mu.Unlock()

		// * log packet info periodically
		if pkt.SequenceNumber%100 == 0 {
			log.Printf("RTP: SSRC=%d, Seq=%d, Size=%d",
				pkt.SSRC, pkt.SequenceNumber, len(pkt.Payload))
		}
	})

	// * start playback
	log.Printf("Starting playback...")
	_, err = s.client.Play(nil)
	if err != nil {
		s.client.Close()
		return fmt.Errorf("RTSP play failed: %w", err)
	}

	log.Printf("RTSP stream active")
	return nil
}

// * stop RTSP connection
func (s *Server) RTSPStop() {
	s.mu.Lock()
	if s.client != nil {
		log.Printf("Stopping RTSP connection...")
		s.client.Close()
		s.client = nil
	}
	s.active = false
	s.mu.Unlock()
}

// * handle index page
func IndexHandler(c fiber.Ctx) error {
	content, err := indexFS.ReadFile("index.html")
	if err != nil {
		c.Status(fiber.StatusNotFound)
		return c.SendString("Index not found")
	}

	c.Set("Content-Type", "text/html")
	return c.Send(content)
}

// * handle raw RTP stream endpoint (chunked binary streaming)
func StreamHandler(server *Server) fiber.Handler {
	return func(c fiber.Ctx) error {
		c.Set("Content-Type", "application/octet-stream")
		c.Set("Cache-Control", "no-cache")
		c.Set("Connection", "keep-alive")
		c.Set("Access-Control-Allow-Origin", "*")

		// * capture context before entering stream writer
		ctx := c.Context()
		reqCtx := c.RequestCtx()

		reqCtx.SetBodyStreamWriter(func(w *bufio.Writer) {
			w.Flush()

			// * stream packets with ticker
			ticker := time.NewTicker(50 * time.Millisecond)
			defer ticker.Stop()

			for {
				select {
				case <-ctx.Done():
					log.Printf("Stream client disconnected")
					return
				case <-ticker.C:
					server.mu.Lock()
					active := server.active
					server.mu.Unlock()

					if !active {
						continue
					}

					// * get all buffered packets
					server.mu.Lock()
					packetsCount := len(server.packets)
					if packetsCount == 0 {
						server.mu.Unlock()
						continue
					}

					// * send latest packet
					pkt := server.packets[packetsCount-1]
					server.mu.Unlock()

					// * write raw binary packet
					w.Write(pkt)
					w.Flush()
				}
			}
		})
		return nil
	}
}

// * get preferred outbound address
func GetOutboundIP() net.IP {
	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil {
		log.Fatal(err)
	}
	defer conn.Close()

	localAddr := conn.LocalAddr().(*net.UDPAddr)
	return localAddr.IP
}

// * main entry point
func main() {
	// * parse CLI
	cli := &CLI{}
	kctx := kong.Parse(cli)

	// * initialize server
	server := ServerNew()

	// * start RTSP if URL provided
	if cli.RTSP != "" {
		err := server.RTSPStart(cli.RTSP)
		if err != nil {
			log.Fatalf("RTSP start failed: %v", err)
		}
		defer server.RTSPStop()
	} else {
		log.Printf("No RTSP URL provided — server will start but stream will be inactive")
		log.Printf("Usage: --rtsp rtsp://your-server/stream")
	}

	// * setup signal handler for graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	// * create Fiber app
	app := fiber.New(fiber.Config{
		BodyLimit: 4 * 1024 * 1024,
		AppName:   "experiment-streamable",
	})

	// * register routes
	app.Get("/", IndexHandler)
	app.Get("/stream", StreamHandler(server))

	// * get outbound IP for display
	ip := GetOutboundIP()
	addr := fmt.Sprintf("%s:%d", ip, cli.Port)

	// * start server in goroutine
	go func() {
		log.Printf("SSE server starting on http://%s", addr)
		log.Printf("Open http://localhost:%d in your browser", cli.Port)
		if err := app.Listen(fmt.Sprintf(":%d", cli.Port)); err != nil && err != net.ErrClosed {
			log.Printf("Server error: %v", err)
		}
	}()

	// * wait for signal
	log.Printf("Server ready — press Ctrl+C to stop")
	<-sigChan

	// * graceful shutdown
	log.Printf("Shutting down...")
	if err := app.ShutdownWithTimeout(5 * time.Second); err != nil {
		log.Printf("Shutdown error: %v", err)
	}
	server.RTSPStop()
	log.Printf("Shutdown complete")

	kctx.Exit(0)
}
