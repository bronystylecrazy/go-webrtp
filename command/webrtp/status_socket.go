package main

import (
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/gofiber/contrib/v3/websocket"
	"github.com/gofiber/fiber/v3"
)

type StatusSocketMessage struct {
	Streams []*StreamApiResponse `json:"streams"`
}

type DashboardRow struct {
	Name             string  `json:"name"`
	Active           bool    `json:"active"`
	Status           string  `json:"status"`
	Codec            string  `json:"codec"`
	Resolution       string  `json:"resolution"`
	Framerate        float64 `json:"framerate"`
	Bitrate          float64 `json:"bitrateKbps"`
	Bandwidth        uint64  `json:"bandwidthBytes"`
	Frames           uint64  `json:"frames"`
	Clients          int32   `json:"clients"`
	Uptime           string  `json:"uptime"`
	Recording        bool    `json:"recording"`
	RecordingMode    string  `json:"recordingMode,omitempty"`
	RecordingOffline string  `json:"recordingOfflineMode,omitempty"`
	RecordingFile    string  `json:"recordingFile,omitempty"`
	RecordingBytes   int64   `json:"recordingBytes"`
}

type DashboardSocketMessage struct {
	Rows []*DashboardRow `json:"rows"`
}

type DeskViewSyncMessage struct {
	Name        string  `json:"name"`
	Quality     string  `json:"quality,omitempty"`
	Distort     bool    `json:"distort"`
	GridEnabled bool    `json:"gridEnabled"`
	DeskEnabled bool    `json:"deskEnabled"`
	FX          float64 `json:"fx"`
	FY          float64 `json:"fy"`
	Scale       float64 `json:"scale"`
	Desk        string  `json:"desk,omitempty"`
	Guides      string  `json:"guides,omitempty"`
}

type DeskViewSocketBroker struct {
	mu       sync.Mutex
	clients  map[string]map[*websocket.Conn]struct{}
	lastByID map[string][]byte
}

func NewDeskViewSocketBroker() *DeskViewSocketBroker {
	return &DeskViewSocketBroker{
		clients:  make(map[string]map[*websocket.Conn]struct{}),
		lastByID: make(map[string][]byte),
	}
}

func (r *DeskViewSocketBroker) add(syncID string, conn *websocket.Conn) []byte {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, ok := r.clients[syncID]; !ok {
		r.clients[syncID] = make(map[*websocket.Conn]struct{})
	}
	r.clients[syncID][conn] = struct{}{}
	if payload, ok := r.lastByID[syncID]; ok {
		return append([]byte(nil), payload...)
	}
	return nil
}

func (r *DeskViewSocketBroker) remove(syncID string, conn *websocket.Conn) {
	r.mu.Lock()
	defer r.mu.Unlock()

	clients := r.clients[syncID]
	if clients == nil {
		return
	}
	delete(clients, conn)
	if len(clients) == 0 {
		delete(r.clients, syncID)
	}
}

func (r *DeskViewSocketBroker) publish(syncID string, payload []byte) {
	r.mu.Lock()
	clients := make([]*websocket.Conn, 0, len(r.clients[syncID]))
	for conn := range r.clients[syncID] {
		clients = append(clients, conn)
	}
	r.lastByID[syncID] = append([]byte(nil), payload...)
	r.mu.Unlock()

	for _, conn := range clients {
		_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
		if err := conn.WriteMessage(websocket.TextMessage, payload); err != nil {
			r.remove(syncID, conn)
			_ = conn.Close()
		}
	}
}

func DeskViewSocketHandler(broker *DeskViewSocketBroker) fiber.Handler {
	return func(c fiber.Ctx) error {
		if !websocket.IsWebSocketUpgrade(c) {
			return fiber.ErrUpgradeRequired
		}
		syncID := c.Query("sync")
		if syncID == "" {
			return fiber.NewError(fiber.StatusBadRequest, "missing sync")
		}
		return websocket.New(func(conn *websocket.Conn) {
			defer conn.Close()
			lastPayload := broker.add(syncID, conn)
			defer broker.remove(syncID, conn)

			if len(lastPayload) > 0 {
				_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
				if err := conn.WriteMessage(websocket.TextMessage, lastPayload); err != nil {
					return
				}
			}

			for {
				messageType, payload, err := conn.ReadMessage()
				if err != nil {
					return
				}
				if messageType != websocket.TextMessage {
					continue
				}
				msg := &DeskViewSyncMessage{}
				if err := json.Unmarshal(payload, msg); err != nil {
					continue
				}
				normalized, err := json.Marshal(msg)
				if err != nil {
					continue
				}
				broker.publish(syncID, normalized)
			}
		})(c)
	}
}

func StatusSocketHandler(manager *StreamManager) fiber.Handler {
	return func(c fiber.Ctx) error {
		if !websocket.IsWebSocketUpgrade(c) {
			return fiber.ErrUpgradeRequired
		}
		return websocket.New(func(conn *websocket.Conn) {
			defer conn.Close()

			done := make(chan struct{})
			go func() {
				defer close(done)
				for {
					if _, _, err := conn.ReadMessage(); err != nil {
						return
					}
				}
			}()

			send := func() error {
				items := manager.StreamStatusList()
				StreamResponsesSort(items)
				_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
				return conn.WriteJSON(&StatusSocketMessage{Streams: items})
			}

			if err := send(); err != nil {
				return
			}

			ticker := time.NewTicker(time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-done:
					return
				case <-ticker.C:
					if err := send(); err != nil {
						return
					}
				}
			}
		})(c)
	}
}

func DashboardSocketHandler(manager *StreamManager) fiber.Handler {
	return func(c fiber.Ctx) error {
		if !websocket.IsWebSocketUpgrade(c) {
			return fiber.ErrUpgradeRequired
		}
		return websocket.New(func(conn *websocket.Conn) {
			defer conn.Close()

			done := make(chan struct{})
			go func() {
				defer close(done)
				for {
					if _, _, err := conn.ReadMessage(); err != nil {
						return
					}
				}
			}()

			send := func() error {
				streams := manager.StreamListExpanded()
				rows := make([]*DashboardRow, 0, len(streams))
				for _, stream := range streams {
					stats := stream.Hub.GetStats(tuiStreamDisplayName(stream))
					recording := stream.Inst.RecordingStatus()
					status := "Ready"
					if !stats.Ready {
						status = "Waiting"
					}
					rows = append(rows, &DashboardRow{
						Name:             tuiStreamDisplayName(stream),
						Active:           stats.ClientCount > 0 || recording.Active,
						Status:           status,
						Codec:            stats.Codec,
						Resolution:       fmt.Sprintf("%dx%d", stats.Width, stats.Height),
						Framerate:        stats.Framerate,
						Bitrate:          stats.Bitrate,
						Bandwidth:        stats.BytesRecv,
						Frames:           stats.FrameNo,
						Clients:          stats.ClientCount,
						Uptime:           formatUptime(stats.Uptime),
						Recording:        recording.Active,
						RecordingOffline: recording.OfflineMode,
						RecordingFile:    recording.Path,
						RecordingBytes:   recording.BytesWritten,
					})
				}
				_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
				return conn.WriteJSON(&DashboardSocketMessage{Rows: rows})
			}

			if err := send(); err != nil {
				return
			}

			ticker := time.NewTicker(time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-done:
					return
				case <-ticker.C:
					if err := send(); err != nil {
						return
					}
				}
			}
		})(c)
	}
}
