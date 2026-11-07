// Package config loads and validates the daemon's JSON configuration. The
// local web UI planned for the LMH is meant to write this same file.
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"lmhcan/internal/protocol"
	"lmhcan/internal/rs485"
	"lmhcan/internal/serialport"
)

type Config struct {
	MQTT  MQTT     `json:"mqtt"`
	Buses []Bus    `json:"buses,omitempty"` // CAN
	RS485 []Serial `json:"rs485,omitempty"` // RS485 / Modbus
}

type MQTT struct {
	Host        string `json:"host"`
	Port        int    `json:"port"`
	ClientID    string `json:"client_id"`
	Username    string `json:"username"`
	Password    string `json:"password"`
	TopicPrefix string `json:"topic_prefix"`
}

// Bus is one CAN interface and the single device on it. One device per
// interface is enforced because the supported protocols use fixed frame IDs
// with no sender address, so two devices on one bus would overwrite each other.
type Bus struct {
	Interface string `json:"interface"` // as shown by `ip -br link`, e.g. can1
	Bitrate   int    `json:"bitrate"`
	Device    Device `json:"device"`
}

// Serial is one RS485 port and the single device on it. One device per port
// is enforced for now.
type Serial struct {
	Port     string `json:"port"`      // e.g. /dev/ttyACM1
	Baud     int    `json:"baud"`      // default: the protocol's
	Parity   string `json:"parity"`    // N (default), E or O
	StopBits int    `json:"stop_bits"` // 1 (default) or 2
	Slave    int    `json:"slave"`     // device address 1..247, default: the protocol's
	Device   Device `json:"device"`
}

// Line returns the serial line format.
func (s Serial) Line() serialport.Config {
	parity := byte('N')
	if s.Parity != "" {
		parity = s.Parity[0]
	}
	return serialport.Config{Baud: s.Baud, Parity: parity, StopBits: s.StopBits}
}

type Device struct {
	Name        string  `json:"name"`
	Class       string  `json:"class"` // "battery" (CAN or RS485) or "inverter" (RS485 only)
	Protocol    string  `json:"protocol"`
	Topic       string  `json:"topic"`         // optional override of <prefix>/<name>
	IntervalS   float64 `json:"interval_s"`    // default 1
	StaleAfterS float64 `json:"stale_after_s"` // default 10
}

var (
	nameRe     = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
	portRe     = regexp.MustCompile(`^/dev/[A-Za-z0-9_./:+-]+$`)
	ifaceRe    = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,15}$`)
	goodRates  = map[int]bool{10000: true, 20000: true, 50000: true, 100000: true, 125000: true, 250000: true, 500000: true, 800000: true, 1000000: true}
	errNoBuses = errors.New("config has no buses (need at least one CAN bus or RS485 port)")
)

// Load reads, defaults and validates a config file.
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c Config
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if err := c.Validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &c, nil
}

// Validate applies defaults and checks the whole configuration.
func (c *Config) Validate() error {
	if c.MQTT.Port == 0 {
		c.MQTT.Port = 1883
	}
	if c.MQTT.TopicPrefix == "" {
		c.MQTT.TopicPrefix = "lmh"
	}
	if c.MQTT.ClientID == "" {
		c.MQTT.ClientID = "lmh-can"
	}
	if len(c.Buses) == 0 && len(c.RS485) == 0 {
		return errNoBuses
	}
	ifaces := map[string]bool{}
	names := map[string]bool{}
	for i := range c.Buses {
		b := &c.Buses[i]
		if !ifaceRe.MatchString(b.Interface) {
			return fmt.Errorf("buses[%d]: invalid interface %q", i, b.Interface)
		}
		if ifaces[b.Interface] {
			return fmt.Errorf("buses[%d]: interface %s listed twice (one device per interface)", i, b.Interface)
		}
		ifaces[b.Interface] = true
		if !goodRates[b.Bitrate] {
			return fmt.Errorf("buses[%d] (%s): unsupported bitrate %d", i, b.Interface, b.Bitrate)
		}
		if err := c.checkDevice(fmt.Sprintf("buses[%d] (%s)", i, b.Interface), &b.Device, names, func(n string) error {
			_, err := protocol.Lookup(n)
			return err
		}); err != nil {
			return err
		}
	}
	ports := map[string]bool{}
	for i := range c.RS485 {
		r := &c.RS485[i]
		where := fmt.Sprintf("rs485[%d] (%s)", i, r.Port)
		if !portRe.MatchString(r.Port) {
			return fmt.Errorf("rs485[%d]: invalid port %q (expected a path like /dev/ttyACM1)", i, r.Port)
		}
		if ports[r.Port] {
			return fmt.Errorf("%s: port listed twice (one device per port)", where)
		}
		ports[r.Port] = true
		var defBaud, defSlave int
		switch r.Device.Class {
		case "battery":
			info, err := rs485.Lookup(r.Device.Protocol)
			if err != nil {
				return fmt.Errorf("%s: %w", where, err)
			}
			defBaud, defSlave = info.DefaultBaud, info.DefaultSlave
		case "inverter":
			info, err := rs485.LookupInverter(r.Device.Protocol)
			if err != nil {
				return fmt.Errorf("%s: %w", where, err)
			}
			defBaud, defSlave = info.DefaultBaud, info.DefaultSlave
		default:
			return fmt.Errorf("%s: device %q needs a class: \"battery\" or \"inverter\" (got %q)", where, r.Device.Name, r.Device.Class)
		}
		if r.Baud == 0 {
			r.Baud = defBaud
		}
		if r.Parity == "" {
			r.Parity = "N"
		}
		if r.StopBits == 0 {
			r.StopBits = 1
		}
		if r.Slave == 0 {
			r.Slave = defSlave
		}
		if r.Slave < 1 || r.Slave > 247 {
			return fmt.Errorf("%s: slave address %d must be 1..247", where, r.Slave)
		}
		if err := r.Line().Validate(); err != nil {
			return fmt.Errorf("%s: %w", where, err)
		}
		if err := c.checkDevice(where, &r.Device, names, func(string) error { return nil }); err != nil {
			return err
		}
	}
	return nil
}

// checkDevice validates the parts of a device shared by every link type and
// fills in defaults. lookup checks the protocol name.
func (c *Config) checkDevice(where string, d *Device, names map[string]bool, lookup func(string) error) error {
	if !nameRe.MatchString(d.Name) {
		return fmt.Errorf("%s: device name %q must be letters, digits, '-' or '_'", where, d.Name)
	}
	if names[d.Name] {
		return fmt.Errorf("%s: device name %q used twice", where, d.Name)
	}
	names[d.Name] = true
	if d.Class == "" {
		return fmt.Errorf("%s: device %q needs a class: \"battery\" or \"inverter\"", where, d.Name)
	}
	if d.Class != "battery" && d.Class != "inverter" {
		return fmt.Errorf("%s: device class %q is not implemented yet (battery and inverter only)", where, d.Class)
	}
	if d.Class == "inverter" && !strings.HasPrefix(where, "rs485") {
		return fmt.Errorf("%s: inverters are only supported over RS485 so far", where)
	}
	if err := lookup(d.Protocol); err != nil {
		return fmt.Errorf("%s: %w", where, err)
	}
	if d.IntervalS == 0 {
		d.IntervalS = 1
	}
	if d.StaleAfterS == 0 {
		d.StaleAfterS = 10
	}
	if d.IntervalS < 0.1 || d.StaleAfterS < d.IntervalS {
		return fmt.Errorf("%s: need interval_s >= 0.1 and stale_after_s >= interval_s", where)
	}
	if d.Topic == "" {
		d.Topic = c.MQTT.TopicPrefix + "/" + d.Name
	}
	return nil
}

func (d Device) Interval() time.Duration { return time.Duration(d.IntervalS * float64(time.Second)) }
func (d Device) StaleAfter() time.Duration {
	return time.Duration(d.StaleAfterS * float64(time.Second))
}
