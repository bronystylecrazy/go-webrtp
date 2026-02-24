package main

import (
	"context"
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
	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/cors"
	"github.com/mattn/go-isatty"
	"gopkg.in/yaml.v3"
)

var CLI struct {
	Config    string `help:"Config file path" short:"c" default:"config.yml"`
	Interface bool   `help:"Use graphical interface" short:"i" default:"false"`
	Port      int    `help:"HTTP server port" short:"p" default:"8080"`
}

type Config struct {
	Upstreams []*Upstream `yaml:"upstreams"`
}

type Upstream struct {
	Name    *string `yaml:"name"`
	RtspUrl string  `yaml:"rtspUrl" validate:"required"`
}

type Stream struct {
	Name    string
	URL     string
	Inst    *webrtp.Instance
	Hub     *webrtp.Hub
	Handler fiber.Handler
}

type TUI struct {
	streams  []*Stream
	page     int
	pageSize int
	stats    []webrtp.StreamStats
	logs     []string
	quitting bool
}

func (m *TUI) Init() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg {
		return tickMsg{t}
	})
}

type tickMsg struct{ time.Time }

func (m *TUI) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
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
		return m, tea.Tick(time.Second, func(t time.Time) tea.Msg {
			return tickMsg{t}
		})
	}
	return m, nil
}

var (
	headerStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("86")).Bold(true)
	greenStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("82"))
	yellowStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("226"))
	dimStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
)

func (m *TUI) View() string {
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
			name,
			status,
			stats.Codec,
			fmt.Sprintf("%dx%d", stats.Width, stats.Height),
			fmt.Sprintf("%.1f", stats.Fps),
			fmt.Sprintf("%d", stats.ClientCount),
			fmt.Sprintf("%.1f kbps", stats.Bitrate),
			fmt.Sprintf("%.2f MB", float64(stats.BytesRecv)/1024/1024),
			formatUptime(stats.Uptime),
		})
	}

	t := table.New(
		table.WithColumns([]table.Column{
			{Title: "#", Width: 3},
			{Title: "Name", Width: 15},
			{Title: "Status", Width: 10},
			{Title: "Codec", Width: 8},
			{Title: "Resolution", Width: 12},
			{Title: "FPS", Width: 8},
			{Title: "Clients", Width: 8},
			{Title: "Bitrate", Width: 12},
			{Title: "Transferred", Width: 12},
			{Title: "Uptime", Width: 10},
		}),
		table.WithRows(rows),
		table.WithFocused(false),
	)

	s := table.DefaultStyles()
	s.Header = headerStyle
	s.Cell = lipgloss.NewStyle()
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
		headerStyle.Render("WebTransport RTSP Streams"),
		t.View(),
		nav,
		"",
		headerStyle.Render("Logs"),
		logsView,
	)
}

func formatUptime(d time.Duration) string {
	if d < time.Minute {
		return d.Round(time.Second).String()
	}
	if d < time.Hour {
		return fmt.Sprintf("%.1fm", d.Minutes())
	}
	return fmt.Sprintf("%.1fh", d.Hours())
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
		if u.RtspUrl == "" {
			return nil, fmt.Errorf("upstream missing required rtspUrl")
		}
	}

	return &cfg, nil
}

func main() {
	kong.Parse(&CLI)

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
		inst := webrtp.Init(&webrtp.Config{
			RTSP:   u.RtspUrl,
			Logger: log.Default(),
		})
		streams[i] = &Stream{
			Name:    name,
			URL:     u.RtspUrl,
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

	app.Get("/", func(c fiber.Ctx) error {
		return c.JSON(fiber.Map{
			"streams": func() []fiber.Map {
				result := make([]fiber.Map, len(streams))
				for i, s := range streams {
					stats := s.Hub.GetStats(s.Name)
					result[i] = fiber.Map{
						"name":         s.Name,
						"url":          s.URL,
						"ready":        stats.Ready,
						"clients":      stats.ClientCount,
						"bitrate_kbps": stats.Bitrate,
						"bytes":        stats.BytesRecv,
						"uptime":       stats.Uptime.String(),
					}
				}
				return result
			}(),
		})
	})

	if CLI.Interface && isatty.IsTerminal(os.Stdout.Fd()) {
		runTUI(ctx, streams)
	} else {
		runServer(ctx, streams, app, CLI.Port)
	}
}

func runServer(ctx context.Context, streams []*Stream, app *fiber.App, port int) {
	go func() {
		addr := fmt.Sprintf(":%d", port)
		log.Printf("HTTP server listening on http://localhost%s", addr)
		log.Printf("Streams available:")
		for i, s := range streams {
			log.Printf("  - /stream/no/%d (%s) -> %s", i, s.Name, s.URL)
		}
		if err := app.Listen(addr); err != nil {
			log.Printf("HTTP: %v", err)
		}
	}()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	<-sigChan
	log.Println("shutting down")
	for _, s := range streams {
		s.Inst.Stop()
	}
}

func runTUI(ctx context.Context, streams []*Stream) {
	m := &TUI{
		streams:  streams,
		pageSize: 10,
		logs:     []string{},
	}

	p := tea.NewProgram(m, tea.WithAltScreen())

	// Create a log writer that sends to TUI
	logWriter := &logWriter{logs: &m.logs}
	log.SetOutput(logWriter)
	log.SetFlags(0)

	if _, err := p.Run(); err != nil {
		log.Fatalf("TUI: %v", err)
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
