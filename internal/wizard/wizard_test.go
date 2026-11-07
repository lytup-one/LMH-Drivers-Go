package wizard

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"lmhcan/internal/config"
)

type harness struct {
	out   bytes.Buffer
	ran   [][]string
	files map[string][]byte
	env   Env
}

func newHarness(input string, existing ...string) *harness {
	h := &harness{files: map[string][]byte{}}
	h.env = Env{
		In:            bufio.NewReader(strings.NewReader(input)),
		Out:           &h.out,
		CANInterfaces: func() []Iface { return []Iface{{"can0", "down"}, {"can1", "up"}} },
		SerialPorts:   func() []string { return []string{"/dev/ttyACM0", "/dev/ttyACM1"} },
		Run:           func(a ...string) error { h.ran = append(h.ran, a); return nil },
		WriteFile:     func(p string, d []byte) error { h.files[p] = d; return nil },
		Exists: func(p string) bool {
			for _, e := range existing {
				if e == p {
					return true
				}
			}
			return false
		},
	}
	return h
}

func lines(l ...string) string { return strings.Join(l, "\n") + "\n" }

func TestHappyPathGrowattOnCan1(t *testing.T) {
	// device BMS, link CAN, protocol growatt, bitrate default, interface 2 (can1),
	// name default, listen-only yes, MQTT no, path, proceed, apply, "Done, just save" (2 of 2 without MQTT)
	h := newHarness(lines("1", "1", "growatt", "", "2", "", "", "", "/tmp/x.json", "", "", "2"))
	res, err := Run(h.env)
	if err != nil {
		t.Fatalf("%v\n%s", err, h.out.String())
	}
	if res.Interface != "can1" || res.Protocol != "growatt" || res.Action != ActionSave || res.MQTT {
		t.Errorf("result = %+v", res)
	}
	want := [][]string{
		{"ip", "link", "set", "can1", "down"},
		{"ip", "link", "set", "can1", "type", "can", "bitrate", "500000", "listen-only", "on"},
		{"ip", "link", "set", "can1", "up"},
	}
	if !reflect.DeepEqual(h.ran, want) {
		t.Errorf("commands = %v", h.ran)
	}
	var cfg config.Config
	if err := json.Unmarshal(h.files["/tmp/x.json"], &cfg); err != nil {
		t.Fatal(err)
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("saved config does not validate: %v", err)
	}
	b := cfg.Buses[0]
	if b.Interface != "can1" || b.Bitrate != 500000 || b.Device.Protocol != "growatt" || b.Device.Name != "bms1" || b.Device.Class != "battery" {
		t.Errorf("bus = %+v", b)
	}
}

func TestMQTTAndOtherBitrate(t *testing.T) {
	// bitrate "other" -> 250000, MQTT yes
	h := newHarness(lines("bms", "can", "pylontech", "other", "can0", "250000", "pack-a", "n", "y",
		"broker.local", "1884", "plant", "/tmp/y.json", "", "n", "2"))
	res, err := Run(h.env)
	if err != nil {
		t.Fatalf("%v\n%s", err, h.out.String())
	}
	if len(h.ran) != 0 {
		t.Errorf("must not run commands when declined: %v", h.ran)
	}
	if !res.MQTT || res.Action != ActionService {
		t.Errorf("result = %+v", res)
	}
	var cfg config.Config
	json.Unmarshal(h.files["/tmp/y.json"], &cfg)
	cfg.Validate()
	if cfg.MQTT.Host != "broker.local" || cfg.MQTT.Port != 1884 || cfg.Buses[0].Device.Topic != "plant/pack-a" || cfg.Buses[0].Bitrate != 250000 {
		t.Errorf("config = %+v", cfg)
	}
	if !strings.Contains(h.out.String(), "listen-only false") {
		t.Errorf("listen-only off must be in the summary:\n%s", h.out.String())
	}
}

func TestListenOnlyOffCommand(t *testing.T) {
	c := IfaceCommands("can1", 250000, false)
	if c[1][len(c[1])-1] != "off" || c[1][7] != "250000" {
		t.Errorf("commands = %v", c)
	}
}

func TestUnsupportedProtocolIsRejectedThenReasked(t *testing.T) {
	h := newHarness(lines("1", "1", "deye", "victron", "", "1", "", "", "", "/tmp/z.json", "", "n", "2"))
	res, err := Run(h.env)
	if err != nil {
		t.Fatalf("%v\n%s", err, h.out.String())
	}
	if res.Protocol != "victron" || !strings.Contains(h.out.String(), "deye is not supported yet") {
		t.Errorf("protocol = %s\n%s", res.Protocol, h.out.String())
	}
}

func TestNotAvailableChoices(t *testing.T) {
	for _, in := range []string{lines("3")} {
		h := newHarness(in)
		if _, err := Run(h.env); !errors.Is(err, ErrNotAvailable) {
			t.Errorf("input %q: err = %v, want ErrNotAvailable", in, err)
		}
	}
}

func TestInvalidChoiceAndNameAreReasked(t *testing.T) {
	// "9" is not a device; bad device name "a b" is rejected then "ok-1" accepted
	h := newHarness(lines("9", "1", "1", "pylontech", "", "1", "a b", "ok-1", "", "", "/tmp/w.json", "", "", "2"))
	res, err := Run(h.env)
	if err != nil {
		t.Fatalf("%v\n%s", err, h.out.String())
	}
	var cfg config.Config
	json.Unmarshal(h.files[res.ConfigPath], &cfg)
	if cfg.Buses[0].Device.Name != "ok-1" || !strings.Contains(h.out.String(), "Not a valid choice") {
		t.Errorf("name = %q\n%s", cfg.Buses[0].Device.Name, h.out.String())
	}
}

func TestOverwriteDeclinedAndEOF(t *testing.T) {
	h := newHarness(lines("1", "1", "pylontech", "", "1", "", "", "", "/tmp/exists.json", "n"), "/tmp/exists.json")
	if _, err := Run(h.env); !errors.Is(err, ErrAborted) {
		t.Errorf("declined overwrite: %v", err)
	}
	if len(h.files) != 0 {
		t.Error("nothing may be written")
	}
	if _, err := Run(newHarness("1\n1\n").env); !errors.Is(err, ErrAborted) {
		t.Errorf("EOF must abort, got %v", err)
	}
}

func TestNoInterfacesFoundAsksForName(t *testing.T) {
	h := newHarness(lines("1", "1", "pylontech", "", "can7", "", "", "", "/tmp/n.json", "", "n", "2"))
	h.env.CANInterfaces = func() []Iface { return nil }
	res, err := Run(h.env)
	if err != nil || res.Interface != "can7" {
		t.Fatalf("%+v %v\n%s", res, err, h.out.String())
	}
}

func TestRS485PaceModbusHappyPath(t *testing.T) {
	// BMS, RS485, protocol pacemodbus, port 2 (ttyACM1), baud default, format default,
	// address default, name default, MQTT no, path, proceed, "Show live data" (1)
	h := newHarness(lines("1", "2", "pacemodbus", "2", "", "", "", "", "n", "/tmp/r.json", "", "1"))
	res, err := Run(h.env)
	if err != nil {
		t.Fatalf("%v\n%s", err, h.out.String())
	}
	if res.Class != "battery" || res.Link != "rs485" || res.Interface != "/dev/ttyACM1" || res.Protocol != "pacemodbus" ||
		res.Baud != 9600 || res.Parity != "N" || res.StopBits != 1 || res.Slave != 1 || res.Action != ActionLive {
		t.Errorf("result = %+v", res)
	}
	if len(h.ran) != 0 {
		t.Errorf("RS485 setup must not run privileged commands: %v", h.ran)
	}
	var cfg config.Config
	if err := json.Unmarshal(h.files["/tmp/r.json"], &cfg); err != nil {
		t.Fatal(err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("saved config does not validate: %v", err)
	}
	if len(cfg.Buses) != 0 || len(cfg.RS485) != 1 {
		t.Fatalf("config = %+v", cfg)
	}
	r := cfg.RS485[0]
	if r.Port != "/dev/ttyACM1" || r.Baud != 9600 || r.Slave != 1 || r.Device.Name != "bms1" || r.Device.Protocol != "pacemodbus" {
		t.Errorf("serial = %+v", r)
	}
}

func TestRS485CustomLineMQTTAndBadAnswers(t *testing.T) {
	// unsupported protocol (growatt) rejected; port "other" (2) -> typed path; baud 19200 (2);
	// format 8E1 (2); slave "abc" rejected then 5; name "bad name" rejected (slave is asked
	// again with it) then "pack-b"; MQTT yes; run with MQTT (2 of 3)
	h := newHarness(lines("bms", "rs485", "growatt", "pacemodbus", "2", "/dev/ttyUSB0", "2", "2",
		"abc", "5", "bad name", "5", "pack-b", "y", "broker", "", "plant", "/tmp/s.json", "", "2"))
	h.env.SerialPorts = func() []string { return []string{"/dev/ttyACM1"} }
	res, err := Run(h.env)
	if err != nil {
		t.Fatalf("%v\n%s", err, h.out.String())
	}
	if res.Interface != "/dev/ttyUSB0" || res.Baud != 19200 || res.Parity != "E" || res.Slave != 5 || !res.MQTT || res.Action != ActionService {
		t.Errorf("result = %+v", res)
	}
	out := h.out.String()
	for _, want := range []string{"growatt is not supported yet", `Not a number: "abc"`, "Problem:", "8E1, address 5"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	var cfg config.Config
	json.Unmarshal(h.files["/tmp/s.json"], &cfg)
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	if cfg.RS485[0].Device.Topic != "plant/pack-b" || cfg.MQTT.Host != "broker" || cfg.MQTT.Port != 1883 {
		t.Errorf("config = %+v", cfg)
	}
}

func TestRS485NoPortsFoundAsksForPath(t *testing.T) {
	h := newHarness(lines("1", "2", "pacemodbus", "/dev/ttyACM3", "", "", "", "", "n", "/tmp/p.json", "", "2"))
	h.env.SerialPorts = func() []string { return nil }
	res, err := Run(h.env)
	if err != nil || res.Interface != "/dev/ttyACM3" {
		t.Fatalf("%+v %v\n%s", res, err, h.out.String())
	}
}

func TestRS485InverterHappyPath(t *testing.T) {
	// device inverter (no link question), protocol srne, port 2, baud / format / address / name default,
	// MQTT no, path, proceed, "Show live data"
	h := newHarness(lines("2", "srne", "2", "", "", "", "", "n", "/tmp/inv.json", "", "1"))
	res, err := Run(h.env)
	if err != nil {
		t.Fatalf("%v\n%s", err, h.out.String())
	}
	if res.Class != "inverter" || res.Link != "rs485" || res.Protocol != "srne" || res.Interface != "/dev/ttyACM1" || res.Baud != 9600 || res.Slave != 1 || res.Action != ActionLive {
		t.Errorf("result = %+v", res)
	}
	var cfg config.Config
	if err := json.Unmarshal(h.files["/tmp/inv.json"], &cfg); err != nil {
		t.Fatal(err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("saved config does not validate: %v", err)
	}
	if d := cfg.RS485[0].Device; d.Class != "inverter" || d.Name != "inv1" || d.Protocol != "srne" {
		t.Errorf("device = %+v", d)
	}
	if !strings.Contains(h.out.String(), "inverter over RS485") {
		t.Errorf("summary lacks the device kind:\n%s", h.out.String())
	}
}
