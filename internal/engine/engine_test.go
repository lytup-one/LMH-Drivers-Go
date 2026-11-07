package engine

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"lmhcan/internal/canbus"
	"lmhcan/internal/model"
	"lmhcan/internal/protocol"
	"lmhcan/internal/publish"
)

func push(t *testing.T, b *canbus.Fake, id uint32, h string) {
	t.Helper()
	d, _ := hex.DecodeString(h)
	b.C <- canbus.Frame{ID: id, Data: d}
}

func lastOf(m *publish.Memory, suffix string) (publish.Message, bool) {
	msgs := m.Messages()
	for i := len(msgs) - 1; i >= 0; i-- {
		if strings.HasSuffix(msgs[i].Topic, suffix) {
			return msgs[i], true
		}
	}
	return publish.Message{}, false
}

func TestEngine(t *testing.T) {
	info, _ := protocol.Lookup("pylontech")
	bus := canbus.NewFake()
	mem := &publish.Memory{}
	dev := Device{Name: "bms1", Protocol: "pylontech", Topic: "lmh/bms1",
		Interval: 20 * time.Millisecond, StaleAfter: 150 * time.Millisecond}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, bus, info.New(), dev, mem, slog.New(slog.NewTextHandler(io.Discard, nil)))
	}()

	// Before any frame: status "waiting", no state.
	time.Sleep(60 * time.Millisecond)
	if m, ok := lastOf(mem, "/status"); !ok || string(m.Payload) != "waiting" {
		t.Fatalf("status before frames: %+v", m)
	}
	if _, ok := lastOf(mem, "/state"); ok {
		t.Fatal("state must not be published before the core values arrive")
	}

	// Only voltage/current: still not ready (SOC missing).
	push(t, bus, 0x356, "DB15F9FF4C010000")
	time.Sleep(60 * time.Millisecond)
	if _, ok := lastOf(mem, "/state"); ok {
		t.Fatal("state published before SOC arrived")
	}

	// Add SOC and a kernel error frame.
	push(t, bus, 0x355, "6300640000000000")
	bus.C <- canbus.Frame{ID: 0x1, Error: true}
	time.Sleep(60 * time.Millisecond)
	m, ok := lastOf(mem, "/state")
	if !ok || !m.Retain {
		t.Fatalf("no retained state: %+v", m)
	}
	var st map[string]interface{}
	if err := json.Unmarshal(m.Payload, &st); err != nil {
		t.Fatal(err)
	}
	if st["voltage_v"] != 55.95 || st["current_a"] != -0.7 || st["soc_pct"] != 99.0 || st["stale"] != false {
		t.Errorf("state = %v", st)
	}
	if st["power_w"] != -39.2 {
		t.Errorf("power_w = %v, want -39.2", st["power_w"])
	}
	if st["bus_errors"] != 1.0 {
		t.Errorf("bus_errors = %v, want 1", st["bus_errors"])
	}
	if s, _ := lastOf(mem, "/status"); string(s.Payload) != "online" {
		t.Errorf("status = %s, want online", s.Payload)
	}

	// Silence -> stale.
	time.Sleep(300 * time.Millisecond)
	if s, _ := lastOf(mem, "/status"); string(s.Payload) != "stale" {
		t.Errorf("status = %s, want stale", s.Payload)
	}
	m, _ = lastOf(mem, "/state")
	json.Unmarshal(m.Payload, &st)
	if st["stale"] != true {
		t.Errorf("stale flag = %v, want true", st["stale"])
	}

	// Frames resume -> online again.
	push(t, bus, 0x356, "DB15F9FF4C010000")
	time.Sleep(60 * time.Millisecond)
	if s, _ := lastOf(mem, "/status"); string(s.Payload) != "online" {
		t.Errorf("status = %s, want online after resume", s.Payload)
	}

	cancel()
	if err := <-done; err != nil {
		t.Errorf("Run returned %v", err)
	}
}

func TestRunPolled(t *testing.T) {
	mem := &publish.Memory{}
	dev := Device{Name: "bms1", Protocol: "pacemodbus", Topic: "lmh/bms1",
		Interval: 20 * time.Millisecond, StaleAfter: 150 * time.Millisecond}
	var mu sync.Mutex
	failing := true
	poll := func(st *model.Battery) error {
		mu.Lock()
		defer mu.Unlock()
		if failing {
			return errors.New("no reply")
		}
		st.VoltageV, st.CurrentA, st.SOCPct = model.F(52), model.F(-10), model.F(80)
		return nil
	}
	setFailing := func(v bool) { mu.Lock(); failing = v; mu.Unlock() }
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- RunPolled(ctx, poll, dev, mem, slog.New(slog.NewTextHandler(io.Discard, nil)))
	}()

	time.Sleep(80 * time.Millisecond)
	if m, ok := lastOf(mem, "/status"); !ok || string(m.Payload) != "waiting" {
		t.Fatalf("status while the BMS is silent: %+v", m)
	}
	if _, ok := lastOf(mem, "/state"); ok {
		t.Fatal("state published before any good poll")
	}

	setFailing(false)
	time.Sleep(80 * time.Millisecond)
	m, ok := lastOf(mem, "/state")
	if !ok {
		t.Fatal("no state after a good poll")
	}
	var st map[string]interface{}
	json.Unmarshal(m.Payload, &st)
	if st["voltage_v"] != 52.0 || st["power_w"] != -520.0 || st["stale"] != false || st["bus_errors"].(float64) < 1 {
		t.Errorf("state = %v", st)
	}
	if s, _ := lastOf(mem, "/status"); string(s.Payload) != "online" {
		t.Errorf("status = %s, want online", s.Payload)
	}

	setFailing(true)
	time.Sleep(300 * time.Millisecond)
	if s, _ := lastOf(mem, "/status"); string(s.Payload) != "stale" {
		t.Errorf("status = %s, want stale", s.Payload)
	}
	m, _ = lastOf(mem, "/state")
	json.Unmarshal(m.Payload, &st)
	if st["stale"] != true || st["voltage_v"] != 52.0 {
		t.Errorf("stale state must keep the last values: %v", st)
	}

	cancel()
	if err := <-done; err != nil {
		t.Errorf("RunPolled returned %v", err)
	}
}

func TestRunSourceInverter(t *testing.T) {
	mem := &publish.Memory{}
	dev := Device{Name: "inv1", Protocol: "srne", Topic: "lmh/inv1", Interval: 20 * time.Millisecond, StaleAfter: 150 * time.Millisecond}
	var mu sync.Mutex
	failing := true
	src := &InverterSource{State: model.Inverter{Device: "inv1", Protocol: "srne"}, PollFn: func(i *model.Inverter) error {
		mu.Lock()
		defer mu.Unlock()
		if failing {
			return errors.New("no reply")
		}
		i.BatteryVoltageV, i.BatteryCurrentA, i.StateCode, i.State = model.F(51.2), model.F(10), model.I(4), "mains powered operation"
		i.PV = []model.PVInput{{VoltageV: 350, CurrentA: 12, PowerW: 4200}}
		return nil
	}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- RunSource(ctx, src, dev, mem, slog.New(slog.NewTextHandler(io.Discard, nil))) }()

	time.Sleep(80 * time.Millisecond)
	if m, ok := lastOf(mem, "/status"); !ok || string(m.Payload) != "waiting" {
		t.Fatalf("status while silent: %+v", m)
	}
	if _, ok := lastOf(mem, "/state"); ok {
		t.Fatal("state published before a good poll")
	}
	mu.Lock()
	failing = false
	mu.Unlock()
	time.Sleep(80 * time.Millisecond)
	m, ok := lastOf(mem, "/state")
	if !ok || !m.Retain {
		t.Fatalf("no retained state: %+v", m)
	}
	var st map[string]interface{}
	json.Unmarshal(m.Payload, &st)
	if st["class"] != "inverter" || st["battery_power_w"] != 512.0 || st["pv_power_w"] != 4200.0 || st["state"] != "mains powered operation" || st["stale"] != false {
		t.Errorf("state = %v", st)
	}
	if st["bus_errors"].(float64) < 1 {
		t.Errorf("bus_errors = %v", st["bus_errors"])
	}
	cancel()
	if err := <-done; err != nil {
		t.Errorf("RunSource returned %v", err)
	}
}
