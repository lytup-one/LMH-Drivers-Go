// Package rs485 holds the protocols a BMS speaks over RS485. Unlike CAN,
// where the BMS broadcasts, RS485 is request / response: the LMH is the master
// and polls the BMS, so a protocol here is a Poller that sends requests and
// decodes the replies.
package rs485

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"lmhcan/internal/modbus"
	"lmhcan/internal/model"
	"lmhcan/internal/protocol"
	"lmhcan/internal/serialport"
)

// Poller reads one complete set of values from the BMS into b. It returns an
// error when the BMS did not answer or answered badly; b is left untouched in
// that case.
type Poller interface {
	Poll(b *model.Battery) error
}

// Info describes one registered RS485 protocol.
type Info struct {
	Name         string
	Level        protocol.Level
	Note         string
	DefaultBaud  int
	DefaultSlave int
	// New creates a poller for the device at address slave (Modbus slave id, or
	// the ADR byte for Pylon-style protocols). nil when Unsupported.
	New func(p serialport.Port, slave byte) Poller
}

const needCapture = "not decoded yet: no specification or capture. To add it: set this protocol on the BMS in PbmsTools, " +
	"connect the LMH in parallel with the inverter on the RS485 lines (A, B and GND) and record " +
	"the traffic with `lmh-can sniff /dev/ttyACM1 -baud 9600 -out capture.txt`, then send the file"

var all = []Info{
	{Name: "pacemodbus", Level: protocol.Verified, DefaultBaud: 9600, DefaultSlave: 1,
		Note: "PACE BMS Modbus RTU V1.3, function 3, registers 0-39 plus version / serial strings; register map checked live with the Python reader",
		New:  func(p serialport.Port, slave byte) Poller { return newPace(&modbus.Client{P: p}, slave) }},
	{Name: "pylon", Level: protocol.Captured, DefaultBaud: 9600, DefaultSlave: 2,
		Note: "Pylontech RS485 ASCII frames (VER 20): analog values, charge/discharge limits, manufacturer, serial; from a capture of this BMS; alarm info (0x44) and system parameters (0x47) got no reply",
		New:  func(p serialport.Port, slave byte) Poller { return newPylon(p, slave) }},
	{Name: "growatt", Level: protocol.Unsupported, Note: needCapture, DefaultBaud: 9600},
	{Name: "voltronic", Level: protocol.Captured, DefaultBaud: 9600, DefaultSlave: 1,
		Note: "Voltronic's \"Inverter and BMS 485 communication Protocol\" (2020): Modbus-like frames with a 2-byte word-count length; checked on this BMS against its Pylon / PACE readings; cell voltages are only 0.1 V resolution (no cell spread); alarm / protect words never non-zero so far",
		New:  func(p serialport.Port, slave byte) Poller { return newVoltronic(p, slave) }},
	{Name: "se", Level: protocol.Unsupported, Note: needCapture, DefaultBaud: 9600},
	{Name: "luxpower_vo3", Level: protocol.Captured, DefaultBaud: 9600, DefaultSlave: 1,
		Note: "Luxpowertek battery protocol RS485 V0.3: Modbus function 3, 16 registers, little-endian request and data words; checked on this BMS (the standard big-endian request is rejected); full capacity is whole Ah only; fault / warning codes raw, seen only at zero",
		New:  func(p serialport.Port, slave byte) Poller { return newLuxpower(p, slave) }},
	{Name: "wow", Level: protocol.Captured, DefaultBaud: 9600, DefaultSlave: 1,
		Note: "Modbus RTU, registers 0-39 as in the PACE map (checked on the BMS with a register dump; the vendor's WOW table has wrong scales); flags raw only; slave 1 at 9600 8N1 answered",
		New:  func(p serialport.Port, slave byte) Poller { return newWow(&modbus.Client{P: p}, slave) }},
	{Name: "pylon_f", Level: protocol.Unsupported, Note: needCapture, DefaultBaud: 9600},
}

var aliases = map[string]string{
	"pace": "pacemodbus", "pace_modbus": "pacemodbus", "pace modbus": "pacemodbus", "pace-modbus": "pacemodbus",
	"luxpower": "luxpower_vo3", "luxpower vo3": "luxpower_vo3", "pylon f": "pylon_f", "pylon-f": "pylon_f",
}

// Lookup finds an RS485 protocol by name (case-insensitive, aliases accepted).
// Unsupported protocols are returned together with an error wrapping
// protocol.ErrNotImplemented.
func Lookup(name string) (Info, error) {
	n := strings.ToLower(strings.TrimSpace(name))
	if a, ok := aliases[n]; ok {
		n = a
	}
	for _, i := range all {
		if i.Name == n {
			if i.Level == protocol.Unsupported {
				return i, fmt.Errorf("%w: rs485 %s: %s", protocol.ErrNotImplemented, i.Name, i.Note)
			}
			return i, nil
		}
	}
	names := make([]string, 0, len(all))
	for _, i := range all {
		names = append(names, i.Name)
	}
	sort.Strings(names)
	return Info{}, fmt.Errorf("unknown RS485 protocol %q (known: %s)", name, strings.Join(names, ", "))
}

// All returns every RS485 protocol in display order.
func All() []Info { return append([]Info(nil), all...) }

// IsUnsupported reports whether err means "protocol known but not implemented".
func IsUnsupported(err error) bool { return errors.Is(err, protocol.ErrNotImplemented) }
