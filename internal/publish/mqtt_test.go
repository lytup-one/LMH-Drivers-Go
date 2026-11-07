package publish

import (
	"io"
	"log/slog"
	"net"
	"strings"
	"testing"
	"time"
)

// A tiny fake broker: accepts one connection, answers CONNACK, and records
// the CONNECT body and every PUBLISH packet it receives.
func TestMQTTConnectAndPublish(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	connect := make(chan []byte, 1)
	pubs := make(chan []byte, 8)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		readPacket := func() (byte, []byte, error) {
			var h [1]byte
			if _, err := io.ReadFull(c, h[:]); err != nil {
				return 0, nil, err
			}
			n, mult := 0, 1
			for {
				var b [1]byte
				if _, err := io.ReadFull(c, b[:]); err != nil {
					return 0, nil, err
				}
				n += int(b[0]&0x7F) * mult
				if b[0]&0x80 == 0 {
					break
				}
				mult *= 128
			}
			body := make([]byte, n)
			_, err := io.ReadFull(c, body)
			return h[0], body, err
		}
		h, body, err := readPacket()
		if err != nil || h != 0x10 {
			return
		}
		connect <- body
		c.Write([]byte{0x20, 0x02, 0x00, 0x00})
		for {
			h, body, err := readPacket()
			if err != nil {
				return
			}
			if h&0xF0 == 0x30 {
				pubs <- append([]byte{h}, body...)
			}
		}
	}()

	addr := ln.Addr().(*net.TCPAddr)
	connected := make(chan struct{}, 1)
	m := NewMQTT(MQTTConfig{
		Host: "127.0.0.1", Port: addr.Port, ClientID: "test-client",
		Username: "u", Password: "p",
		WillTopic: "lmh/lmh-can/status", WillPayload: "offline",
		OnConnect: func(m *MQTT) { connected <- struct{}{} },
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	defer m.Close()

	select {
	case <-connected:
	case <-time.After(3 * time.Second):
		t.Fatal("never connected")
	}
	body := string(<-connect)
	for _, want := range []string{"MQTT", "test-client", "lmh/lmh-can/status", "offline", "u", "p"} {
		if !strings.Contains(body, want) {
			t.Errorf("CONNECT body missing %q", want)
		}
	}
	// protocol level 4, flags: clean session | will | will retain | password | username
	if body[6] != 4 || body[7] != 0x02|0x04|0x20|0x40|0x80 {
		t.Errorf("connect level/flags = %d/0x%02X", body[6], body[7])
	}

	if err := m.Publish("lmh/bms1/state", []byte(`{"a":1}`), true); err != nil {
		t.Fatal(err)
	}
	select {
	case p := <-pubs:
		if p[0] != 0x31 { // PUBLISH, retain
			t.Errorf("fixed header = 0x%02X, want 0x31", p[0])
		}
		if !strings.Contains(string(p), "lmh/bms1/state") || !strings.HasSuffix(string(p), `{"a":1}`) {
			t.Errorf("packet = %q", p)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("publish not received")
	}
}

func TestLenCodec(t *testing.T) {
	for _, n := range []int{0, 1, 127, 128, 16383, 16384, 2097151} {
		br := byteReader(encodeLen(n))
		got, err := decodeLen(&br)
		if err != nil || got != n {
			t.Errorf("len %d -> %d, %v", n, got, err)
		}
	}
}

type byteReader []byte

func (b *byteReader) ReadByte() (byte, error) {
	if len(*b) == 0 {
		return 0, io.EOF
	}
	v := (*b)[0]
	*b = (*b)[1:]
	return v, nil
}

func TestJSONLinesOnlyPrintsState(t *testing.T) {
	var sb strings.Builder
	j := &JSONLines{W: &sb}
	j.Publish("listen/status", []byte("online"), true)
	j.Publish("listen/state", []byte(`{"a":1}`), true)
	if sb.String() != "{\"a\":1}\n" {
		t.Errorf("output = %q", sb.String())
	}
}
