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

// InverterPoller reads one complete set of values from an inverter into i. It
// returns an error when the inverter did not answer or answered badly.
type InverterPoller interface {
	Poll(i *model.Inverter) error
}

// InverterInfo describes one registered RS485 inverter protocol.
type InverterInfo struct {
	Name         string
	Level        protocol.Level
	Note         string
	DefaultBaud  int
	DefaultSlave int
	New          func(p serialport.Port, slave byte) InverterPoller
}

var inverters = []InverterInfo{
	{Name: "srne", Level: protocol.Unverified, DefaultBaud: 9600, DefaultSlave: 1,
		Note: "SRNE energy storage inverter Modbus protocol V1.7 (also the HFP / HF series): battery, PV, grid, output, load, temperatures, faults, energy statistics; from the vendor document, not yet run on a real inverter",
		New:  func(p serialport.Port, slave byte) InverterPoller { return newSRNE(&modbus.Client{P: p}, slave) }},
}

// LookupInverter finds an RS485 inverter protocol by name.
func LookupInverter(name string) (InverterInfo, error) {
	n := strings.ToLower(strings.TrimSpace(name))
	for _, i := range inverters {
		if i.Name == n {
			return i, nil
		}
	}
	names := make([]string, 0, len(inverters))
	for _, i := range inverters {
		names = append(names, i.Name)
	}
	sort.Strings(names)
	return InverterInfo{}, fmt.Errorf("unknown RS485 inverter protocol %q (known: %s)", name, strings.Join(names, ", "))
}

// AllInverters returns every RS485 inverter protocol in display order.
func AllInverters() []InverterInfo { return append([]InverterInfo(nil), inverters...) }

// CheckDevice checks that protocol name belongs to the given device kind
// ("battery" or "inverter"). The kind is never inferred: it must be given, and
// the protocol is looked up only in that kind's own list.
func CheckDevice(device, name string) error {
	switch strings.ToLower(strings.TrimSpace(device)) {
	case "battery":
		b, err := Lookup(name)
		if b.Name != "" || err == nil { // an unsupported protocol still has a name; poll reports it
			return nil
		}
		if _, ierr := LookupInverter(name); ierr == nil {
			return fmt.Errorf("%q is an inverter protocol, not a battery one: use -device inverter", name)
		}
		return err
	case "inverter":
		_, err := LookupInverter(name)
		if err != nil {
			if b, _ := Lookup(name); b.Name != "" {
				return fmt.Errorf("%q is a battery protocol, not an inverter one: use -device battery", name)
			}
		}
		return err
	case "":
		return errors.New("-device is required: battery or inverter")
	}
	return fmt.Errorf("unknown device %q (use battery or inverter)", device)
}
