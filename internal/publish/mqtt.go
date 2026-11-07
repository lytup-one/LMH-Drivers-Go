package publish

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strconv"
	"sync"
	"time"
)

// MQTTConfig configures the built-in MQTT 3.1.1 client.
type MQTTConfig struct {
	Host, ClientID, Username, Password string
	Port                               int
	KeepAlive                          time.Duration // default 30 s
	// Last-will: published (retained) by the broker if this client vanishes.
	WillTopic, WillPayload string
	// OnConnect is called (from the connection goroutine) after every successful
	// connect, e.g. to publish an "online" status over the last-will.
	OnConnect func(m *MQTT)
}

// MQTT is a small publish-only MQTT 3.1.1 client (QoS 0) that reconnects by
// itself. It is deliberately minimal so the daemon has no third-party
// dependencies; publishing while disconnected returns an error and the
// message is dropped (state is re-published every interval anyway).
type MQTT struct {
	cfg    MQTTConfig
	log    *slog.Logger
	mu     sync.Mutex
	conn   net.Conn
	closed chan struct{}
	once   sync.Once
}

// NewMQTT starts the connection loop in the background and returns immediately.
func NewMQTT(cfg MQTTConfig, log *slog.Logger) *MQTT {
	if cfg.Port == 0 {
		cfg.Port = 1883
	}
	if cfg.KeepAlive == 0 {
		cfg.KeepAlive = 30 * time.Second
	}
	if log == nil {
		log = slog.Default()
	}
	m := &MQTT{cfg: cfg, log: log, closed: make(chan struct{})}
	go m.loop()
	return m
}

func (m *MQTT) loop() {
	backoff := time.Second
	for {
		select {
		case <-m.closed:
			return
		default:
		}
		conn, err := m.connect()
		if err != nil {
			m.log.Warn("mqtt connect failed", "err", err, "retry_in", backoff)
			select {
			case <-m.closed:
				return
			case <-time.After(backoff):
			}
			if backoff < 30*time.Second {
				backoff *= 2
			}
			continue
		}
		backoff = time.Second
		m.mu.Lock()
		m.conn = conn
		m.mu.Unlock()
		m.log.Info("mqtt connected", "broker", fmt.Sprintf("%s:%d", m.cfg.Host, m.cfg.Port))
		if m.cfg.OnConnect != nil {
			m.cfg.OnConnect(m)
		}
		err = m.serve(conn)
		m.mu.Lock()
		m.conn = nil
		m.mu.Unlock()
		conn.Close()
		select {
		case <-m.closed:
			return
		default:
			m.log.Warn("mqtt connection lost", "err", err)
		}
	}
}

func (m *MQTT) connect() (net.Conn, error) {
	addr := net.JoinHostPort(m.cfg.Host, strconv.Itoa(m.cfg.Port))
	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		return nil, err
	}
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := conn.Write(m.connectPacket()); err != nil {
		conn.Close()
		return nil, err
	}
	var ack [4]byte
	if _, err := io.ReadFull(conn, ack[:]); err != nil {
		conn.Close()
		return nil, err
	}
	if ack[0] != 0x20 || ack[1] != 2 || ack[3] != 0 {
		conn.Close()
		return nil, fmt.Errorf("broker refused connection (CONNACK code %d)", ack[3])
	}
	conn.SetDeadline(time.Time{})
	return conn, nil
}

func (m *MQTT) connectPacket() []byte {
	flags := byte(0x02) // clean session
	var payload []byte
	payload = appendString(payload, m.cfg.ClientID)
	if m.cfg.WillTopic != "" {
		flags |= 0x04 | 0x20 // will + will retain, QoS 0
		payload = appendString(payload, m.cfg.WillTopic)
		payload = appendString(payload, m.cfg.WillPayload)
	}
	if m.cfg.Username != "" {
		flags |= 0x80
		payload = appendString(payload, m.cfg.Username)
		if m.cfg.Password != "" {
			flags |= 0x40
			payload = appendString(payload, m.cfg.Password)
		}
	}
	var vh []byte
	vh = appendString(vh, "MQTT")
	vh = append(vh, 4, flags, byte(int(m.cfg.KeepAlive.Seconds())>>8), byte(int(m.cfg.KeepAlive.Seconds())))
	body := append(vh, payload...)
	return append(append([]byte{0x10}, encodeLen(len(body))...), body...)
}

// serve keeps the connection alive and drains incoming packets until it fails.
func (m *MQTT) serve(conn net.Conn) error {
	errc := make(chan error, 1)
	go func() {
		r := bufio.NewReader(conn)
		for {
			conn.SetReadDeadline(time.Now().Add(2 * m.cfg.KeepAlive))
			if _, err := r.ReadByte(); err != nil {
				errc <- err
				return
			}
			n, err := decodeLen(r)
			if err != nil {
				errc <- err
				return
			}
			if _, err := r.Discard(n); err != nil {
				errc <- err
				return
			}
		}
	}()
	t := time.NewTicker(m.cfg.KeepAlive / 2)
	defer t.Stop()
	for {
		select {
		case err := <-errc:
			return err
		case <-m.closed:
			return nil
		case <-t.C:
			m.mu.Lock()
			conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
			_, err := conn.Write([]byte{0xC0, 0x00}) // PINGREQ
			m.mu.Unlock()
			if err != nil {
				return err
			}
		}
	}
}

// Publish sends a QoS 0 message.
func (m *MQTT) Publish(topic string, payload []byte, retain bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.conn == nil {
		return errors.New("mqtt: not connected")
	}
	hdr := byte(0x30)
	if retain {
		hdr |= 0x01
	}
	body := appendString(nil, topic)
	body = append(body, payload...)
	pkt := append(append([]byte{hdr}, encodeLen(len(body))...), body...)
	m.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if _, err := m.conn.Write(pkt); err != nil {
		m.conn.Close() // makes serve() fail and trigger a reconnect
		return err
	}
	return nil
}

// Close publishes nothing further and disconnects cleanly.
func (m *MQTT) Close() error {
	m.once.Do(func() { close(m.closed) })
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.conn != nil {
		m.conn.Write([]byte{0xE0, 0x00}) // DISCONNECT (broker discards the will)
		m.conn.Close()
		m.conn = nil
	}
	return nil
}

func appendString(b []byte, s string) []byte {
	b = append(b, byte(len(s)>>8), byte(len(s)))
	return append(b, s...)
}

func encodeLen(n int) []byte {
	var out []byte
	for {
		d := byte(n % 128)
		n /= 128
		if n > 0 {
			d |= 0x80
		}
		out = append(out, d)
		if n == 0 {
			return out
		}
	}
}

func decodeLen(r io.ByteReader) (int, error) {
	n, mult := 0, 1
	for i := 0; i < 4; i++ {
		b, err := r.ReadByte()
		if err != nil {
			return 0, err
		}
		n += int(b&0x7F) * mult
		if b&0x80 == 0 {
			return n, nil
		}
		mult *= 128
	}
	return 0, errors.New("mqtt: malformed remaining length")
}
