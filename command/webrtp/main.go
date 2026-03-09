package main

import (
	"context"
	_ "embed"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/alecthomas/kong"
	"github.com/charmbracelet/bubbles/table"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/connectedtechco/go-webrtp"
	"github.com/dustin/go-humanize"
	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/cors"
	"github.com/mattn/go-isatty"
	"gopkg.in/yaml.v3"
)

//go:embed index.html
var indexHtml []byte

//go:embed index.css
var indexCss []byte

var CLI struct {
	Config         string `help:"Config file path" short:"c" default:"config.yml"`
	Interface      bool   `help:"Use graphical interface" short:"i" default:"false"`
	Port           int    `help:"HTTP server port" short:"p" default:"8080"`
	ListUsbDevices bool   `help:"List available USB video devices and exit" default:"false"`
}

type Config struct {
	TelemetryServiceName *string     `yaml:"telemetryServiceName"`
	TelemetryEndpoint    *string     `yaml:"telemetryEndpoint"`
	Upstreams            []*Upstream `yaml:"upstreams"`
}

type Upstream struct {
	Name       *string  `yaml:"name"`
	SourceType *string  `yaml:"sourceType"`
	RtspUrl    string   `yaml:"rtspUrl"`
	Device     string   `yaml:"device"`
	Codec      string   `yaml:"codec"`
	FrameRate  *float64 `yaml:"frameRate"`
}

type Stream struct {
	Name    string
	Url     string
	Inst    *webrtp.Instance
	Hub     *webrtp.Hub
	Handler fiber.Handler
}

type tickMsg struct{ time.Time }

type Model struct {
	streams      []*Stream
	page         int
	pageSize     int
	stats        []webrtp.StreamStats
	logs         []string
	quitting     bool
	windowWidth  int
	windowHeight int
}

func (m *Model) Init() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg {
		return tickMsg{t}
	})
}

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.windowWidth = msg.Width
		m.windowHeight = msg.Height
		if msg.Height > 18 {
			m.pageSize = msg.Height - 18
		}
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			m.quitting = true
			return m, tea.Quit
		case "left", "h":
			if m.page > 0 {
				m.page--
			}
		case "right", "l":
			if (m.page+1)*m.pageSize < len(m.streams) {
				m.page++
			}
		}
	case tickMsg:
		m.stats = nil
		for _, s := range m.streams {
			m.stats = append(m.stats, s.Hub.GetStats(s.Name))
		}
		MetricsUpdate(m.streams)
		return m, tea.Tick(time.Second, func(t time.Time) tea.Msg {
			return tickMsg{t}
		})
	}
	return m, nil
}

var (
	headerStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("86")).Bold(true)
	whiteStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("255")).Bold(true)
	dimStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
)

func (m *Model) View() string {
	if m.quitting {
		return "Goodbye!\n"
	}

	var rows []table.Row
	m.stats = nil

	start := m.page * m.pageSize
	end := start + m.pageSize
	if end > len(m.streams) {
		end = len(m.streams)
	}

	for i := start; i < end && i < len(m.streams); i++ {
		s := m.streams[i]
		stats := s.Hub.GetStats(s.Name)

		status := "Ready"
		if !stats.Ready {
			status = "Waiting"
		}

		name := s.Name
		if name == strconv.Itoa(i) {
			name = "N/A"
		}

		rows = append(rows, table.Row{
			strconv.Itoa(i),
			truncateCell(name, 15),
			truncateCell(status, 10),
			truncateCell(stats.Codec, 8),
			truncateCell(fmt.Sprintf("%dx%d", stats.Width, stats.Height), 12),
			truncateCell(fmt.Sprintf("%.1f", stats.Framerate), 11),
			truncateCell(fmt.Sprintf("%.1f kbps", stats.Bitrate), 14),
			truncateCell(humanize.Bytes(stats.BytesRecv), 12),
			truncateCell(fmt.Sprintf("%d", stats.FrameNo), 11),
			truncateCell(fmt.Sprintf("%d", stats.ClientCount), 9),
			formatUptime(stats.Uptime),
		})
	}

	// Calculate table height based on window size
	// Layout: header(5) + logs(10) + nav(3) = ~18 fixed lines
	tableHeight := 10 // default
	if m.windowHeight > 18 {
		tableHeight = m.windowHeight - 18
	}

	t := table.New(
		table.WithColumns([]table.Column{
			{Title: "#", Width: 3},
			{Title: "Name", Width: 15},
			{Title: "Status", Width: 10},
			{Title: "Codec", Width: 8},
			{Title: "Resolution", Width: 12},
			{Title: "Framerate", Width: 11},
			{Title: "Bitrate", Width: 14},
			{Title: "Bandwidth", Width: 12},
			{Title: "Frames", Width: 11},
			{Title: "Clients", Width: 9},
			{Title: "Uptime", Width: 10},
		}),
		table.WithRows(rows),
		table.WithHeight(tableHeight),
		table.WithFocused(false),
	)

	s := table.DefaultStyles()
	s.Header = headerStyle
	s.Cell = lipgloss.NewStyle()
	s.Selected = lipgloss.Style{}
	t.SetStyles(s)

	totalPages := (len(m.streams) + m.pageSize - 1) / m.pageSize
	nav := dimStyle.Render(fmt.Sprintf("Page %d/%d (←/→ to navigate, q to quit)", m.page+1, totalPages))

	// Build logs view (last 10 lines)
	var logsView string
	if len(m.logs) > 0 {
		start := len(m.logs) - 10
		if start < 0 {
			start = 0
		}
		logsView = dimStyle.Render(strings.Join(m.logs[start:], "\n"))
	}

	return lipgloss.JoinVertical(lipgloss.Left,
		whiteStyle.Render("┌────────────────────────────────────────┐"),
		whiteStyle.Render("│ WebRTP Streamer                        │"),
		lipgloss.JoinHorizontal(lipgloss.Left,
			whiteStyle.Render("│"),
			dimStyle.Render(" © 2026 Connected Tech Co.,Ltd.         "),
			whiteStyle.Render("│"),
		),
		whiteStyle.Render("└────────────────────────────────────────┘"),
		t.View(),
		nav,
		"",
		headerStyle.Render("Logs"),
		logsView,
	)
}

func formatUptime(d time.Duration) string {
	days := int(d.Hours() / 24)
	hours := int(d.Hours()) % 24
	minutes := int(d.Minutes()) % 60
	seconds := int(d.Seconds()) % 60

	if days > 0 {
		return fmt.Sprintf("%dd %02d:%02d:%02d", days, hours, minutes, seconds)
	}
	return fmt.Sprintf("%02d:%02d:%02d", hours, minutes, seconds)
}

func truncateCell(s string, maxWidth int) string {
	if lipgloss.Width(s) <= maxWidth {
		return s
	}
	return strings.TrimRight(s[:maxWidth-2], " ") + "… "
}

func loadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read file: %w", err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse yaml: %w", err)
	}

	if len(cfg.Upstreams) == 0 {
		return nil, fmt.Errorf("no upstreams defined in config")
	}

	for _, u := range cfg.Upstreams {
		sourceType := "rtsp"
		if u.SourceType != nil && *u.SourceType != "" {
			sourceType = strings.ToLower(*u.SourceType)
		}
		switch sourceType {
		case "rtsp":
			if u.RtspUrl == "" {
				return nil, fmt.Errorf("rtsp upstream missing required rtspUrl")
			}
		case "usb":
			if u.Device == "" {
				return nil, fmt.Errorf("usb upstream missing required device")
			}
			if u.Codec == "" {
				return nil, fmt.Errorf("usb upstream missing required codec")
			}
		default:
			return nil, fmt.Errorf("unsupported sourceType: %s", sourceType)
		}
	}

	return &cfg, nil
}

func main() {
	kong.Parse(&CLI)

	if CLI.ListUsbDevices {
		devices, err := webrtp.UsbDeviceList()
		if err != nil {
			log.Fatalf("list usb devices: %v", err)
		}
		if len(devices) == 0 {
			log.Printf("No USB video devices found")
			return
		}
		for _, device := range devices {
			log.Printf("%s\t%s", device.Id, device.Name)
		}
		return
	}

	cfg, err := loadConfig(CLI.Config)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	streams := make([]*Stream, len(cfg.Upstreams))
	for i, u := range cfg.Upstreams {
		name := strconv.Itoa(i)
		if u.Name != nil && *u.Name != "" {
			name = *u.Name
		}
		prefix := fmt.Sprintf("[#%d: %s]", i, name)
		logger := NewLogger(prefix, log.Default())
		sourceType := "rtsp"
		if u.SourceType != nil && *u.SourceType != "" {
			sourceType = strings.ToLower(*u.SourceType)
		}
		frameRate := 0.0
		if u.FrameRate != nil {
			frameRate = *u.FrameRate
		}
		url := u.RtspUrl
		if sourceType == "usb" {
			url = u.Device
		}
		inst := webrtp.Init(&webrtp.Config{
			SourceType: sourceType,
			Rtsp:       u.RtspUrl,
			Device:     u.Device,
			Codec:      u.Codec,
			FrameRate:  frameRate,
			Logger:     logger,
		})
		streams[i] = &Stream{
			Name:    name,
			Url:     url,
			Inst:    inst,
			Hub:     inst.GetHub(),
			Handler: inst.Handler(),
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Connect all streams to RTSP
	for _, s := range streams {
		go func(ss *Stream) {
			if err := ss.Inst.Connect(); err != nil {
				log.Printf("stream %s: %v", ss.Name, err)
			}
		}(s)
	}

	// Create single fiber instance
	app := fiber.New()
	app.Use(cors.New())

	// Register routes
	for i, s := range streams {
		idx := i
		app.All(fmt.Sprintf("/stream/no/%d", idx), func(c fiber.Ctx) error {
			return streams[idx].Handler(c)
		})
		app.All(fmt.Sprintf("/stream/%s", s.Name), func(c fiber.Ctx) error {
			return streams[idx].Handler(c)
		})
	}

	app.Get("/info", func(c fiber.Ctx) error {
		stats := make([]*webrtp.StreamStats, len(streams))
		for i, s := range streams {
			streamStats := s.Hub.GetStats(s.Name)
			streamStats.Name = s.Name
			stats[i] = &streamStats
		}
		MetricsUpdate(streams)
		return c.JSON(webrtp.Status{Streams: stats})
	})

	app.Get("/", func(c fiber.Ctx) error {
		return c.Type("html").Send(indexHtml)
	})

	app.Get("/index.css", func(c fiber.Ctx) error {
		c.Set("Content-Type", "text/css")
		return c.Send(indexCss)
	})

	// Init metrics
	serviceName := "webrtp"
	if cfg.TelemetryServiceName != nil && *cfg.TelemetryServiceName != "" {
		serviceName = *cfg.TelemetryServiceName
	}
	endpoint := ""
	if cfg.TelemetryEndpoint != nil && *cfg.TelemetryEndpoint != "" {
		endpoint = *cfg.TelemetryEndpoint
	}
	if err := MetricsInit(serviceName, endpoint); err != nil {
		log.Printf("metrics init: %v", err)
	} else {
		MetricsRoute(app)
	}

	// Start fiber server
	go func() {
		addr := fmt.Sprintf(":%d", CLI.Port)
		log.Printf("HTTP server listening on http://localhost%s", addr)
		log.Printf("Streams available:")
		for i, s := range streams {
			log.Printf("  - /stream/no/%d (%s) -> %s", i, s.Name, s.Url)
		}
		if err := app.Listen(addr); err != nil {
			log.Printf("HTTP: %v", err)
		}
	}()

	if CLI.Interface && isatty.IsTerminal(os.Stdout.Fd()) {
		runTUI(ctx, streams)
	} else {
		runServer(ctx, streams)
	}
}

func runServer(ctx context.Context, streams []*Stream) {
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	<-sigChan
	log.Println("shutting down")
	for _, s := range streams {
		s.Inst.Stop()
	}
}

func runTUI(ctx context.Context, streams []*Stream) {
	m := &Model{
		streams:  streams,
		pageSize: 10,
		logs:     []string{},
	}

	p := tea.NewProgram(m, tea.WithAltScreen())

	// Create a log writer that sends to Model
	logWriter := &logWriter{logs: &m.logs}
	log.SetOutput(logWriter)
	log.SetFlags(0)

	if _, err := p.Run(); err != nil {
		log.Fatalf("Model: %v", err)
	}
}

type logWriter struct {
	logs *[]string
}

func (w *logWriter) Write(p []byte) (n int, err error) {
	msg := strings.TrimSpace(string(p))
	if msg != "" {
		*w.logs = append(*w.logs, msg)
		// Keep only last 100 logs
		if len(*w.logs) > 100 {
			*w.logs = (*w.logs)[len(*w.logs)-100:]
		}
	}
	return len(p), nil
}
