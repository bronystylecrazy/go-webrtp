package main

import (
	"crypto/tls"
	"embed"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/alecthomas/kong"
	"github.com/bluenviron/gortsplib/v5"
	"github.com/bluenviron/gortsplib/v5/pkg/base"
	"github.com/bluenviron/gortsplib/v5/pkg/description"
	"github.com/bluenviron/gortsplib/v5/pkg/format"
	"github.com/pion/rtp"
	"github.com/quic-go/quic-go/http3"
	"github.com/quic-go/webtransport-go"
)

// * embed index.html
//
//go:embed index.html
var indexFS embed.FS

// * cli configuration
type CLI struct {
	RTSP string `help:"RTSP URL to stream from" default:"" short:"r"`
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
func IndexHandler(w http.ResponseWriter, _ *http.Request) {
	content, err := indexFS.ReadFile("index.html")
	if err != nil {
		http.Error(w, "Index not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "text/html")
	w.Write(content)
}

// * handle WebTransport stream endpoint
// * let webtransport-go handle the upgrade detection — do NOT check headers manually
func WebTransportHandler(wtServer *webtransport.Server, server *Server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// * attempt WebTransport upgrade; webtransport-go will return error
		// * if this is not a valid CONNECT request
		session, err := wtServer.Upgrade(w, r)
		if err != nil {
			log.Printf("WebTransport upgrade failed (falling back to SSE): %v", err)
			// * fallback to SSE for non-WebTransport requests
			StreamHandler(w, r, server)
			return
		}

		log.Printf("WebTransport connection from %s", r.RemoteAddr)

		// * handle session in goroutine
		go HandleWebTransportSession(session, server)
	}
}

// * handle WebTransport session
func HandleWebTransportSession(session *webtransport.Session, server *Server) {
	defer session.CloseWithError(0, "done")

	log.Printf("WebTransport session established")

	// * stream RTP packets via datagrams at ~20fps / 50ms tick
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	ctx := session.Context()
	lastSeq := -1

	for {
		select {
		case <-ctx.Done():
			log.Printf("WebTransport session closed by client")
			return
		case <-ticker.C:
			server.mu.Lock()
			active := server.active
			pkts := server.packets
			server.mu.Unlock()

			if !active || len(pkts) == 0 {
				continue
			}

			// * send all new packets since last tick (burst mode)
			server.mu.Lock()
			total := len(server.packets)
			server.mu.Unlock()

			if total == lastSeq {
				continue
			}

			// * send the latest packet
			server.mu.Lock()
			pkt := server.packets[total-1]
			server.mu.Unlock()

			lastSeq = total

			err := session.SendDatagram(pkt)
			if err != nil {
				log.Printf("Datagram send error: %v", err)
				return
			}
		}
	}
}

// * handle stream endpoint (SSE fallback for non-WebTransport browsers)
func StreamHandler(w http.ResponseWriter, r *http.Request, server *Server) {
	// * set SSE headers
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	// * flusher for SSE
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming not supported", http.StatusInternalServerError)
		return
	}

	// * send initial event
	fmt.Fprintf(w, "event: connected\ndata: Stream connected\n\n")
	flusher.Flush()

	// * stream packets
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-r.Context().Done():
			log.Printf("SSE client disconnected")
			return
		case <-ticker.C:
			server.mu.Lock()
			active := server.active
			count := len(server.packets)
			server.mu.Unlock()

			if !active {
				fmt.Fprintf(w, "event: waiting\ndata: Waiting for RTSP stream...\n\n")
				flusher.Flush()
				continue
			}

			// * send packet info via SSE
			fmt.Fprintf(w, "event: packet\ndata: {\"count\": %d}\n\n", count)
			flusher.Flush()
		}
	}
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

	// * get certificate paths
	certDir := filepath.Join(".local")
	certFile := filepath.Join(certDir, "x509-cer.pem")
	keyFile := filepath.Join(certDir, "x509-key.pem")

	// * load certificates
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		log.Fatalf("Certificate load failed: %v\n\nGenerate certs with:\n  mkdir -p .local\n  openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:P-256 -keyout .local/x509-key.pem -out .local/x509-cer.pem -days 365 -nodes -subj '/CN=localhost'", err)
	}

	// * create TLS config
	// * NextProtos must include h3 for QUIC/HTTP3
	tlsConfig := &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS13, // * QUIC requires TLS 1.3
		NextProtos:   []string{"h3", "h3-29"},
	}

	// * create mux
	mux := http.NewServeMux()

	// * serve index.html at root
	mux.HandleFunc("/", IndexHandler)

	// * create HTTP/3 server with datagrams enabled (required for WebTransport)
	h3Server := &http3.Server{
		TLSConfig:       tlsConfig,
		Addr:            ":1443",
		Handler:         mux,
		EnableDatagrams: true,
	}

	// * create WebTransport server wrapping the HTTP/3 server
	// * CheckOrigin: return true to allow all origins in dev
	wtServer := &webtransport.Server{
		H3: h3Server,
		CheckOrigin: func(r *http.Request) bool {
			return true
		},
	}

	// * register /stream handler AFTER wtServer is created
	// * so the closure captures the fully initialized wtServer
	mux.HandleFunc("/stream", WebTransportHandler(wtServer, server))

	// * print SPKI hash for Chrome flag
	log.Printf("=== Chrome Launch Command ===")
	log.Printf(`"/Applications/Google Chrome Canary.app/Contents/MacOS/Google Chrome Canary" \`)
	log.Printf(`  --origin-to-force-quic-on=localhost:1443 \`)
	log.Printf(`  --ignore-certificate-errors-spki-list=$(openssl x509 -in .local/x509-cer.pem -pubkey -noout | openssl pkey -pubin -outform der | openssl dgst -sha256 -binary | base64)`)
	log.Printf("=============================")

	// * start WebTransport server (this drives the HTTP/3 server internally)
	go func() {
		log.Printf("WebTransport server starting on :1443 (UDP+TCP)")
		log.Printf("Open https://localhost:1443 in Chrome Canary")
		err := wtServer.ListenAndServe()
		if err != nil && err != http.ErrServerClosed {
			log.Printf("Server error: %v", err)
		}
	}()

	// * wait for signal
	log.Printf("Server ready — press Ctrl+C to stop")
	<-sigChan

	// * graceful shutdown
	log.Printf("Shutting down...")
	_ = wtServer.Close()
	server.RTSPStop()
	log.Printf("Shutdown complete")

	kctx.Exit(0)
}
