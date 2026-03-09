package main

import (
	"fmt"
	"time"

	"github.com/gofiber/contrib/v3/websocket"
	"github.com/gofiber/fiber/v3"
)

type StatusSocketMessage struct {
	Streams []*StreamApiResponse `json:"streams"`
}

type DashboardRow struct {
	Name       string  `json:"name"`
	Active     bool    `json:"active"`
	Status     string  `json:"status"`
	Codec      string  `json:"codec"`
	Resolution string  `json:"resolution"`
	Framerate  float64 `json:"framerate"`
	Bitrate    float64 `json:"bitrateKbps"`
	Bandwidth  uint64  `json:"bandwidthBytes"`
	Frames     uint64  `json:"frames"`
	Clients    int32   `json:"clients"`
	Uptime     string  `json:"uptime"`
}

type DashboardSocketMessage struct {
	Rows []*DashboardRow `json:"rows"`
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
					status := "Ready"
					if !stats.Ready {
						status = "Waiting"
					}
					rows = append(rows, &DashboardRow{
						Name:       tuiStreamDisplayName(stream),
						Active:     stats.ClientCount > 0,
						Status:     status,
						Codec:      stats.Codec,
						Resolution: fmt.Sprintf("%dx%d", stats.Width, stats.Height),
						Framerate:  stats.Framerate,
						Bitrate:    stats.Bitrate,
						Bandwidth:  stats.BytesRecv,
						Frames:     stats.FrameNo,
						Clients:    stats.ClientCount,
						Uptime:     formatUptime(stats.Uptime),
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
