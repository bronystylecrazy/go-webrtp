package webrtp

import (
	"crypto/rand"
	"crypto/tls"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/url"
	"strings"
	"sync"
	"time"
)

type mqttPublisher struct {
	logger   Logger
	topic    string
	addr     string
	clientID string
	username string
	password string
	tls      bool

	mu   sync.Mutex
	conn net.Conn
}

func newMQTTPublisher(cfg *Config, logger Logger) (*mqttPublisher, error) {
	if cfg == nil {
		return nil, fmt.Errorf("missing config")
	}
	rawURL := strings.TrimSpace(cfg.KeyframeMqttURL)
	if rawURL == "" {
		return nil, fmt.Errorf("missing keyframeMqttURL")
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("parse url: %w", err)
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "mqtt" && scheme != "mqtts" {
		return nil, fmt.Errorf("unsupported mqtt scheme %q", u.Scheme)
	}
	host := u.Host
	if !strings.Contains(host, ":") {
		if scheme == "mqtts" {
			host += ":8883"
		} else {
			host += ":1883"
		}
	}
	topic := strings.TrimSpace(cfg.KeyframeMqttTopic)
	if topic == "" {
		streamName := sanitizeName(cfg.StreamName)
		if streamName == "" {
			streamName = "stream"
		}
		topic = fmt.Sprintf("webrtp/%s/keyframe", streamName)
	}
	return &mqttPublisher{
		logger:   logger,
		topic:    topic,
		addr:     host,
		clientID: mqttClientID(),
		username: usernameFromURL(u),
		password: passwordFromURL(u),
		tls:      scheme == "mqtts",
	}, nil
}

func (p *mqttPublisher) Topic() string {
	if p == nil {
		return ""
	}
	return p.topic
}

func (p *mqttPublisher) Publish(frameNo uint32, format string, payload []byte) error {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.ensureConnectedLocked(); err != nil {
		return err
	}
	if err := p.writePublishLocked(payload); err != nil {
		_ = p.closeLocked()
		return err
	}
	return nil
}

func (p *mqttPublisher) Close() {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	_ = p.closeLocked()
}

func (p *mqttPublisher) ensureConnectedLocked() error {
	if p.conn != nil {
		return nil
	}
	var conn net.Conn
	var err error
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	if p.tls {
		conn, err = tls.DialWithDialer(dialer, "tcp", p.addr, &tls.Config{MinVersion: tls.VersionTLS12})
	} else {
		conn, err = dialer.Dial("tcp", p.addr)
	}
	if err != nil {
		return fmt.Errorf("dial mqtt broker: %w", err)
	}
	if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		_ = conn.Close()
		return err
	}
	if err := writeMQTTConnect(conn, p.clientID, p.username, p.password); err != nil {
		_ = conn.Close()
		return fmt.Errorf("mqtt connect: %w", err)
	}
	if err := readMQTTConnAck(conn); err != nil {
		_ = conn.Close()
		return fmt.Errorf("mqtt connack: %w", err)
	}
	_ = conn.SetDeadline(time.Time{})
	p.conn = conn
	return nil
}

func (p *mqttPublisher) writePublishLocked(payload []byte) error {
	if p.conn == nil {
		return fmt.Errorf("mqtt publisher not connected")
	}
	if err := p.conn.SetWriteDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return err
	}
	packet := mqttPublishPacket(p.topic, payload)
	_, err := p.conn.Write(packet)
	_ = p.conn.SetWriteDeadline(time.Time{})
	if err != nil {
		return fmt.Errorf("mqtt publish: %w", err)
	}
	return nil
}

func (p *mqttPublisher) closeLocked() error {
	if p.conn == nil {
		return nil
	}
	_ = p.conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
	_, _ = p.conn.Write([]byte{0xE0, 0x00})
	err := p.conn.Close()
	p.conn = nil
	return err
}

func writeMQTTConnect(w io.Writer, clientID, username, password string) error {
	var body []byte
	body = append(body, mqttString("MQTT")...)
	body = append(body, 0x04)
	flags := byte(0x02)
	if username != "" {
		flags |= 0x80
	}
	if password != "" {
		flags |= 0x40
	}
	body = append(body, flags)
	body = append(body, 0x00, 0x3C)
	body = append(body, mqttString(clientID)...)
	if username != "" {
		body = append(body, mqttString(username)...)
	}
	if password != "" {
		body = append(body, mqttString(password)...)
	}
	packet := []byte{0x10}
	packet = append(packet, mqttRemainingLength(len(body))...)
	packet = append(packet, body...)
	_, err := w.Write(packet)
	return err
}

func readMQTTConnAck(r io.Reader) error {
	header := make([]byte, 4)
	if _, err := io.ReadFull(r, header[:2]); err != nil {
		return err
	}
	if header[0] != 0x20 || header[1] != 0x02 {
		return fmt.Errorf("unexpected connack header %#v", header[:2])
	}
	if _, err := io.ReadFull(r, header[2:4]); err != nil {
		return err
	}
	if header[3] != 0x00 {
		return fmt.Errorf("mqtt connect refused rc=%d", header[3])
	}
	return nil
}

func mqttPublishPacket(topic string, payload []byte) []byte {
	var body []byte
	body = append(body, mqttString(topic)...)
	body = append(body, payload...)
	packet := []byte{0x30}
	packet = append(packet, mqttRemainingLength(len(body))...)
	packet = append(packet, body...)
	return packet
}

func mqttString(value string) []byte {
	buf := make([]byte, 2+len(value))
	binary.BigEndian.PutUint16(buf[:2], uint16(len(value)))
	copy(buf[2:], value)
	return buf
}

func mqttRemainingLength(size int) []byte {
	var out []byte
	for {
		encoded := byte(size % 128)
		size /= 128
		if size > 0 {
			encoded |= 0x80
		}
		out = append(out, encoded)
		if size == 0 {
			return out
		}
	}
}

func mqttClientID() string {
	var random [6]byte
	if _, err := rand.Read(random[:]); err != nil {
		return fmt.Sprintf("webrtp-%d", time.Now().UnixNano())
	}
	return "webrtp-" + hex.EncodeToString(random[:])
}

func usernameFromURL(u *url.URL) string {
	if u == nil || u.User == nil {
		return ""
	}
	return u.User.Username()
}

func passwordFromURL(u *url.URL) string {
	if u == nil || u.User == nil {
		return ""
	}
	password, _ := u.User.Password()
	return password
}
