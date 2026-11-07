package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"lmhcan/internal/protocol"
)

func write(t *testing.T, s string) string {
	p := filepath.Join(t.TempDir(), "c.json")
	if err := os.WriteFile(p, []byte(s), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadDefaults(t *testing.T) {
	c, err := Load(write(t, `{"mqtt":{"host":"localhost"},"buses":[{"interface":"can1","bitrate":500000,
		"device":{"name":"bms1","class":"battery","protocol":"pylontech"}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	d := c.Buses[0].Device
	if d.Topic != "lmh/bms1" || d.IntervalS != 1 || d.StaleAfterS != 10 || d.Class != "battery" || c.MQTT.Port != 1883 {
		t.Errorf("defaults not applied: %+v %+v", d, c.MQTT)
	}
}

func TestLoadRejects(t *testing.T) {
	bus := func(iface string, rate string, dev string) string {
		return `{"buses":[{"interface":"` + iface + `","bitrate":` + rate + `,"device":` + dev + `}]}`
	}
	cases := map[string]string{
		"bad bitrate":     bus("can1", "123", `{"name":"a","class":"battery","protocol":"pylontech"}`),
		"bad name":        bus("can1", "500000", `{"name":"a/b","class":"battery","protocol":"pylontech"}`),
		"unknown field":   `{"buses":[],"oops":1}`,
		"no buses":        `{"buses":[]}`,
		"inverter class":  bus("can1", "500000", `{"name":"a","class":"inverter","protocol":"pylontech"}`),
		"unknown proto":   bus("can1", "500000", `{"name":"a","class":"battery","protocol":"nope"}`),
		"two on one port": `{"buses":[{"interface":"can1","bitrate":500000,"device":{"name":"a","class":"battery","protocol":"pylontech"}},{"interface":"can1","bitrate":500000,"device":{"name":"b","class":"battery","protocol":"pylontech"}}]}`,
	}
	for name, js := range cases {
		if _, err := Load(write(t, js)); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
	_, err := Load(write(t, bus("can1", "500000", `{"name":"a","class":"battery","protocol":"deye"}`)))
	if !errors.Is(err, protocol.ErrNotImplemented) || !strings.Contains(err.Error(), "candump") {
		t.Errorf("unsupported protocol must explain how to add it, got: %v", err)
	}
}

func TestRS485Defaults(t *testing.T) {
	c, err := Load(write(t, `{"rs485":[{"port":"/dev/ttyACM1","device":{"name":"bms1","class":"battery","protocol":"PACE Modbus"}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	r := c.RS485[0]
	if r.Baud != 9600 || r.Parity != "N" || r.StopBits != 1 || r.Slave != 1 ||
		r.Device.Topic != "lmh/bms1" || r.Device.Class != "battery" {
		t.Errorf("defaults not applied: %+v", r)
	}
	if l := r.Line(); l.Baud != 9600 || l.Parity != 'N' || l.StopBits != 1 {
		t.Errorf("line = %+v", l)
	}
}

func TestRS485Rejects(t *testing.T) {
	ser := func(port, extra, dev string) string {
		return `{"rs485":[{"port":"` + port + `"` + extra + `,"device":` + dev + `}]}`
	}
	ok := `{"name":"a","class":"battery","protocol":"pacemodbus"}`
	cases := map[string]string{
		"bad port":      ser("ttyACM1", "", ok),
		"bad baud":      ser("/dev/ttyACM1", `,"baud":1234`, ok),
		"bad parity":    ser("/dev/ttyACM1", `,"parity":"X"`, ok),
		"bad stop bits": ser("/dev/ttyACM1", `,"stop_bits":3`, ok),
		"slave 0..247":  ser("/dev/ttyACM1", `,"slave":300`, ok),
		"unknown proto": ser("/dev/ttyACM1", "", `{"name":"a","class":"battery","protocol":"nope"}`),
		"same port twice": `{"rs485":[{"port":"/dev/ttyACM1","device":` + ok + `},` +
			`{"port":"/dev/ttyACM1","device":{"name":"b","class":"battery","protocol":"pacemodbus"}}]}`,
		"name clash with a CAN device": `{"buses":[{"interface":"can1","bitrate":500000,"device":{"name":"a","class":"battery","protocol":"pylontech"}}],` +
			`"rs485":[{"port":"/dev/ttyACM1","device":` + ok + `}]}`,
	}
	for name, js := range cases {
		if _, err := Load(write(t, js)); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
	_, err := Load(write(t, ser("/dev/ttyACM1", "", `{"name":"a","class":"battery","protocol":"growatt"}`)))
	if !errors.Is(err, protocol.ErrNotImplemented) || !strings.Contains(err.Error(), "sniff") {
		t.Errorf("unsupported RS485 protocol must explain how to add it, got: %v", err)
	}
}

func TestCANAndRS485Together(t *testing.T) {
	_, err := Load(write(t, `{"buses":[{"interface":"can1","bitrate":500000,"device":{"name":"a","class":"battery","protocol":"pylontech"}}],
		"rs485":[{"port":"/dev/ttyACM1","device":{"name":"b","class":"battery","protocol":"pacemodbus"}}]}`))
	if err != nil {
		t.Fatal(err)
	}
}

func TestRS485Inverter(t *testing.T) {
	c, err := Load(write(t, `{"rs485":[{"port":"/dev/ttyUSB4","device":{"name":"inv1","class":"inverter","protocol":"srne"}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	r := c.RS485[0]
	if r.Baud != 9600 || r.Slave != 1 || r.Device.Class != "inverter" || r.Device.Topic != "lmh/inv1" {
		t.Errorf("defaults: %+v", r)
	}
	for name, js := range map[string]string{
		"battery protocol as inverter": `{"rs485":[{"port":"/dev/ttyUSB4","device":{"name":"a","class":"inverter","protocol":"pacemodbus"}}]}`,
		"inverter protocol as battery": `{"rs485":[{"port":"/dev/ttyUSB4","device":{"name":"a","class":"battery","protocol":"srne"}}]}`,
		"inverter over CAN":            `{"buses":[{"interface":"can1","bitrate":500000,"device":{"name":"a","class":"inverter","protocol":"srne"}}]}`,
		"missing class (rs485)":        `{"rs485":[{"port":"/dev/ttyUSB4","device":{"name":"a","protocol":"pacemodbus"}}]}`,
		"missing class (can)":          `{"buses":[{"interface":"can1","bitrate":500000,"device":{"name":"a","protocol":"pylontech"}}]}`,
		"generator":                    `{"rs485":[{"port":"/dev/ttyUSB4","device":{"name":"a","class":"generator","protocol":"srne"}}]}`,
	} {
		if _, err := Load(write(t, js)); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}
