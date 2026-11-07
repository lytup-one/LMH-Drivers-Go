// Package publish sends encoded device state out of the daemon.
package publish

import (
	"fmt"
	"io"
	"strings"
	"sync"
)

// Publisher delivers one message. Implementations must be safe for concurrent use.
type Publisher interface {
	Publish(topic string, payload []byte, retain bool) error
	Close() error
}

// Stdout prints "topic payload" lines; useful for bench testing without a broker.
type Stdout struct {
	mu sync.Mutex
	W  io.Writer
}

func (s *Stdout) Publish(topic string, payload []byte, retain bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := fmt.Fprintf(s.W, "%s %s\n", topic, payload)
	return err
}

func (s *Stdout) Close() error { return nil }

// Message is one recorded publish.
type Message struct {
	Topic   string
	Payload []byte
	Retain  bool
}

// Memory records messages; used in tests.
type Memory struct {
	mu   sync.Mutex
	msgs []Message
}

func (m *Memory) Publish(topic string, payload []byte, retain bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.msgs = append(m.msgs, Message{topic, append([]byte(nil), payload...), retain})
	return nil
}

func (m *Memory) Messages() []Message {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]Message(nil), m.msgs...)
}

func (m *Memory) Close() error { return nil }

// JSONLines prints only the device state payloads, one JSON object per line
// (status messages are dropped). It is what `lmh-can listen` uses.
type JSONLines struct {
	mu sync.Mutex
	W  io.Writer
}

func (j *JSONLines) Publish(topic string, payload []byte, retain bool) error {
	if !strings.HasSuffix(topic, "/state") {
		return nil
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	_, err := fmt.Fprintf(j.W, "%s\n", payload)
	return err
}

func (j *JSONLines) Close() error { return nil }
