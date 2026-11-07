// Package engine runs one device: it reads frames from a bus, feeds them to the
// protocol decoder, and publishes the normalized state.
package engine

import (
	"context"
	"encoding/json"
	"log/slog"
	"math"
	"time"

	"lmhcan/internal/canbus"
	"lmhcan/internal/model"
	"lmhcan/internal/protocol"
	"lmhcan/internal/publish"
)

// Device is the runtime configuration of one monitored device.
type Device struct {
	Name       string
	Protocol   string
	Topic      string        // base topic; "/state" and "/status" are appended
	Interval   time.Duration // publish period
	StaleAfter time.Duration // no decoded frame for this long = stale
}

const statusRefresh = 10 * time.Second

// Run blocks until ctx is cancelled or the bus fails. It publishes:
//
//	<topic>/state   JSON state, retained, once the core values have arrived
//	<topic>/status  "waiting" | "online" | "stale" (retained)
//
// "offline" is published as the MQTT last-will by the broker (see main).
func Run(ctx context.Context, bus canbus.Bus, dec protocol.Decoder, dev Device, pub publish.Publisher, log *slog.Logger) error {
	frames := make(chan canbus.Frame, 256)
	errc := make(chan error, 1)
	go func() {
		for {
			f, err := bus.Recv(ctx)
			if err != nil {
				errc <- err
				return
			}
			select {
			case frames <- f:
			case <-ctx.Done():
				return
			}
		}
	}()

	state := model.Battery{Device: dev.Name, Protocol: dev.Protocol}
	var lastDecoded time.Time
	rep := newReporter(dev, pub, log)

	tick := time.NewTicker(dev.Interval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case err := <-errc:
			if ctx.Err() != nil {
				return nil
			}
			return err
		case f := <-frames:
			if f.Error {
				state.BusErrors++
				continue
			}
			if dec.Decode(f, &state) {
				lastDecoded = time.Now()
			}
		case now := <-tick.C:
			rep.report(now, lastDecoded, func(stale bool) any { return batterySnapshot(&state, now, stale) })
		}
	}
}

// batterySnapshot finishes a battery state for publication, or returns nil
// until the core values (voltage, current, SOC) have all arrived.
func batterySnapshot(state *model.Battery, now time.Time, stale bool) any {
	if !state.Ready() {
		return nil
	}
	state.Stale = stale
	state.TS = now.Unix()
	state.PowerW = model.F(math.Round(*state.VoltageV**state.CurrentA*10) / 10)
	out := state.Normalized()
	return &out
}

// PollFunc reads the device once into st (RS485: send requests, decode the
// replies). It returns an error when the device did not answer properly.
type PollFunc func(st *model.Battery) error

// Source is a polled device of any class. Poll reads it once; Snapshot returns
// the value to publish (nil while the core values have not arrived yet).
type Source interface {
	Poll() error
	Snapshot(now time.Time, stale bool, busErrors uint64) any
}

// RunPolled is RunSource for a battery polled through poll.
func RunPolled(ctx context.Context, poll PollFunc, dev Device, pub publish.Publisher, log *slog.Logger) error {
	return RunSource(ctx, &batterySource{poll: poll, state: model.Battery{Device: dev.Name, Protocol: dev.Protocol}}, dev, pub, log)
}

// NewBatterySource polls a battery through poll, starting from the given state (device and protocol names).
func NewBatterySource(poll PollFunc, state model.Battery) Source {
	return &batterySource{poll: poll, state: state}
}

type batterySource struct {
	poll  PollFunc
	state model.Battery
}

func (b *batterySource) Poll() error { return b.poll(&b.state) }
func (b *batterySource) Snapshot(now time.Time, stale bool, errs uint64) any {
	b.state.BusErrors = errs
	return batterySnapshot(&b.state, now, stale)
}

// InverterSource polls an inverter through Poll.
type InverterSource struct {
	PollFn func(i *model.Inverter) error
	State  model.Inverter
}

func (s *InverterSource) Poll() error { return s.PollFn(&s.State) }

// Snapshot finishes the state for publication, or returns nil until the core values have arrived.
func (s *InverterSource) Snapshot(now time.Time, stale bool, errs uint64) any {
	s.State.BusErrors = errs
	if !s.State.Ready() {
		return nil
	}
	s.State.Stale = stale
	s.State.TS = now.Unix()
	out := s.State
	out.Finish()
	return &out
}

// RunSource is Run for request / response links (RS485): every Interval it
// polls, then publishes exactly as Run does. A failed poll counts in
// bus_errors; after StaleAfter without a good poll the device is stale.
func RunSource(ctx context.Context, src Source, dev Device, pub publish.Publisher, log *slog.Logger) error {
	var lastDecoded time.Time
	var busErrors uint64
	rep := newReporter(dev, pub, log)
	fails := 0

	tick := time.NewTicker(dev.Interval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case now := <-tick.C:
			if err := src.Poll(); err != nil {
				if ctx.Err() != nil {
					return nil
				}
				busErrors++
				fails++
				// Log the first failure, then every 30th, so a dead link does not flood the log.
				if fails == 1 || fails%30 == 0 {
					log.Warn("poll failed", "device", dev.Name, "consecutive", fails, "err", err)
				}
			} else {
				if fails > 0 {
					log.Info("poll recovered", "device", dev.Name, "after_failures", fails)
				}
				fails = 0
				lastDecoded = time.Now()
			}
			rep.report(now, lastDecoded, func(stale bool) any { return src.Snapshot(now, stale, busErrors) })
		}
	}
}

// reporter turns the latest state into the status and state MQTT messages.
type reporter struct {
	dev          Device
	pub          publish.Publisher
	log          *slog.Logger
	lastStatus   string
	lastStatusAt time.Time
}

func newReporter(dev Device, pub publish.Publisher, log *slog.Logger) *reporter {
	return &reporter{dev: dev, pub: pub, log: log}
}

func (r *reporter) report(now, lastDecoded time.Time, snapshot func(stale bool) any) {
	status := "waiting"
	stale := false
	if !lastDecoded.IsZero() {
		status = "online"
		if now.Sub(lastDecoded) > r.dev.StaleAfter {
			status, stale = "stale", true
		}
	}
	if status != r.lastStatus || now.Sub(r.lastStatusAt) >= statusRefresh {
		if err := r.pub.Publish(r.dev.Topic+"/status", []byte(status), true); err != nil {
			r.log.Warn("publish status", "err", err)
		}
		if status != r.lastStatus {
			r.log.Info("device status", "device", r.dev.Name, "status", status)
		}
		r.lastStatus, r.lastStatusAt = status, now
	}
	out := snapshot(stale)
	if out == nil {
		return
	}
	payload, err := json.Marshal(out)
	if err != nil {
		r.log.Error("marshal state", "err", err)
		return
	}
	if err := r.pub.Publish(r.dev.Topic+"/state", payload, true); err != nil {
		r.log.Warn("publish state", "err", err)
	}
}
