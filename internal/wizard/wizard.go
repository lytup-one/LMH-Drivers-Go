// Package wizard is the interactive first-time setup: it asks which device,
// which link (CAN or RS485), the protocol and the line settings (CAN: bitrate
// and interface; RS485: serial port, baud, format and device address), applies
// the CAN interface settings, and writes the daemon's config file. Only BMS
// (battery) is implemented; inverter and generator are listed but say so and stop.
package wizard

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"lmhcan/internal/config"
	"lmhcan/internal/protocol"
	"lmhcan/internal/rs485"
)

// ErrAborted means the input ended or the user declined at the final step.
var ErrAborted = errors.New("setup aborted")

// ErrNotAvailable means the chosen device or link is not implemented yet.
var ErrNotAvailable = errors.New("not available yet")

// Iface is a CAN interface offered to the user.
type Iface struct{ Name, State string }

// Env carries everything the wizard touches, so it can be tested without a terminal.
type Env struct {
	In            *bufio.Reader
	Out           io.Writer
	CANInterfaces func() []Iface
	// SerialPorts lists candidate RS485 ports (e.g. /dev/ttyACM1).
	SerialPorts func() []string
	// Run executes a privileged command (args[0] is the program, e.g. "ip").
	Run       func(args ...string) error
	WriteFile func(path string, data []byte) error
	Exists    func(path string) bool
}

// Action is what to do after setup.
type Action int

const (
	ActionSave    Action = iota // only save the config
	ActionLive                  // print live JSON now
	ActionService               // run with MQTT now
)

// Result is the outcome of a completed setup.
type Result struct {
	ConfigPath string
	Link       string // "can" or "rs485"
	Interface  string // CAN interface or serial port path
	Protocol   string
	MQTT       bool
	Action     Action

	// RS485 only.
	Class    string // "battery" or "inverter"
	Baud     int
	Parity   string
	StopBits int
	Slave    int
}

type item struct{ Key, Label string }

// IfaceCommands are the commands that configure a CAN interface.
func IfaceCommands(iface string, bitrate int, listenOnly bool) [][]string {
	lo := "off"
	if listenOnly {
		lo = "on"
	}
	return [][]string{
		{"ip", "link", "set", iface, "down"},
		{"ip", "link", "set", iface, "type", "can", "bitrate", strconv.Itoa(bitrate), "listen-only", lo},
		{"ip", "link", "set", iface, "up"},
	}
}

func (e Env) say(format string, a ...interface{}) { fmt.Fprintf(e.Out, format+"\n", a...) }

func (e Env) read() (string, error) {
	s, err := e.In.ReadString('\n')
	if err != nil && s == "" {
		return "", ErrAborted
	}
	return strings.TrimSpace(s), nil
}

func (e Env) ask(prompt, def string) (string, error) {
	if def != "" {
		fmt.Fprintf(e.Out, "%s [%s]: ", prompt, def)
	} else {
		fmt.Fprintf(e.Out, "%s: ", prompt)
	}
	s, err := e.read()
	if err != nil {
		return "", err
	}
	if s == "" {
		return def, nil
	}
	return s, nil
}

func (e Env) yesNo(prompt string, def bool) (bool, error) {
	d := "y/N"
	if def {
		d = "Y/n"
	}
	for {
		fmt.Fprintf(e.Out, "%s [%s]: ", prompt, d)
		s, err := e.read()
		if err != nil {
			return false, err
		}
		switch strings.ToLower(s) {
		case "":
			return def, nil
		case "y", "yes":
			return true, nil
		case "n", "no":
			return false, nil
		}
		e.say("Please answer y or n.")
	}
}

// menu shows numbered items and returns the chosen index; a number or an
// item's key (case-insensitive) is accepted.
func (e Env) menu(title string, items []item, def int) (int, error) {
	e.say("\n%s", title)
	for i, it := range items {
		mark := " "
		if i == def {
			mark = "*"
		}
		e.say("  %s %2d) %s", mark, i+1, it.Label)
	}
	for {
		s, err := e.ask("Choose", strconv.Itoa(def+1))
		if err != nil {
			return 0, err
		}
		if n, err := strconv.Atoi(s); err == nil && n >= 1 && n <= len(items) {
			return n - 1, nil
		}
		for i, it := range items {
			if strings.EqualFold(s, it.Key) {
				return i, nil
			}
		}
		e.say("Not a valid choice: %q", s)
	}
}

// Run walks the user through the setup.
func Run(env Env) (*Result, error) {
	env.say("LMH device setup")
	env.say("================")

	// 1. device
	d, err := env.menu("What are you connecting?", []item{
		{"bms", "BMS (battery)"},
		{"inverter", "Inverter"},
		{"generator", "Generator"},
	}, 0)
	if err != nil {
		return nil, err
	}
	if d == 2 {
		return nil, fmt.Errorf("%w: the generator driver is not implemented yet; BMS (battery) and inverter work today", ErrNotAvailable)
	}
	if d == 1 {
		env.say("\nInverters are connected over RS485.")
		return runRS485(env, "inverter")
	}

	// 2. link
	m, err := env.menu("How is it connected?", []item{{"can", "CAN"}, {"rs485", "RS485"}}, 0)
	if err != nil {
		return nil, err
	}
	if m == 1 {
		return runRS485(env, "battery")
	}
	return runCAN(env)
}

// runCAN is the CAN branch: protocol, bitrate, interface, name, mode, MQTT, save, apply.
func runCAN(env Env) (*Result, error) {
	var err error
	// 3. protocol
	var pitems []item
	for _, p := range protocol.All() {
		label := fmt.Sprintf("%-10s %s", p.Name, p.Level)
		if p.Level == protocol.Unsupported {
			label = fmt.Sprintf("%-10s not supported yet (no capture)", p.Name)
		}
		pitems = append(pitems, item{p.Name, label})
	}
	var proto protocol.Info
	for {
		i, err := env.menu("Which protocol is the BMS set to? (must match the BMS setting in PbmsTools)", pitems, 0)
		if err != nil {
			return nil, err
		}
		p, err := protocol.Lookup(pitems[i].Key)
		if err != nil {
			env.say("%s is not supported yet: capture it with candump and send the log. Pick another.", pitems[i].Key)
			continue
		}
		proto = p
		break
	}

	// 4. bitrate
	rates := []int{500000, 250000, 125000, 1000000}
	bitems := []item{{"500000", "500000  (default)"}, {"250000", "250000"}, {"125000", "125000"}, {"1000000", "1000000"}, {"other", "other"}}
	bi, err := env.menu("CAN bitrate (bits per second)", bitems, 0)
	if err != nil {
		return nil, err
	}
	bitrate := 0
	if bi < len(rates) {
		bitrate = rates[bi]
	}

	// 5. interface
	var ifaces []Iface
	if env.CANInterfaces != nil {
		ifaces = env.CANInterfaces()
	}
	var iname string
	if len(ifaces) > 0 {
		iitems := make([]item, 0, len(ifaces)+1)
		for _, f := range ifaces {
			iitems = append(iitems, item{f.Name, fmt.Sprintf("%-8s state: %s", f.Name, f.State)})
		}
		iitems = append(iitems, item{"other", "other (type a name)"})
		env.say("\nOn the LMH tested, the back-panel CAN0 terminals appear as can1; check with `ip -br link`.")
		ii, err := env.menu("CAN interface (socket) the BMS is wired to", iitems, 0)
		if err != nil {
			return nil, err
		}
		if ii < len(ifaces) {
			iname = ifaces[ii].Name
		}
	} else {
		env.say("\nNo CAN interfaces found on this system.")
	}
	if iname == "" {
		if iname, err = env.ask("CAN interface name", "can1"); err != nil {
			return nil, err
		}
	}

	// assemble and validate in a loop so bad names / bitrates are re-asked
	cfg := config.Config{Buses: []config.Bus{{Interface: iname, Bitrate: bitrate}}}
	dev := &cfg.Buses[0].Device
	dev.Protocol, dev.Class = proto.Name, "battery"
	for {
		if bitrate == 0 {
			s, err := env.ask("Bitrate in bit/s", "500000")
			if err != nil {
				return nil, err
			}
			n, err := strconv.Atoi(s)
			if err != nil {
				env.say("Not a number: %q", s)
				continue
			}
			cfg.Buses[0].Bitrate = n
		}
		if dev.Name, err = env.ask("\nName for this device (used in the MQTT topic)", "bms1"); err != nil {
			return nil, err
		}
		trial := cfg
		trial.Buses = append([]config.Bus(nil), cfg.Buses...)
		if err := trial.Validate(); err != nil {
			env.say("Problem: %v", err)
			if strings.Contains(err.Error(), "bitrate") {
				bitrate = 0
				cfg.Buses[0].Bitrate = 0
			}
			continue
		}
		break
	}

	// 6. mode
	listen, err := env.yesNo("\nListen-only (the LMH never transmits; recommended for monitoring)?", true)
	if err != nil {
		return nil, err
	}

	// 7. MQTT
	useMQTT, err := env.askMQTT(&cfg)
	if err != nil {
		return nil, err
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	path, err := env.askPath()
	if err != nil {
		return nil, err
	}

	// summary
	b := cfg.Buses[0]
	env.say("\nSummary")
	env.say("  device    BMS (battery) over CAN, name %q", b.Device.Name)
	env.say("  protocol  %s (%s)", proto.Name, proto.Level)
	env.say("  interface %s at %d bit/s, listen-only %v", b.Interface, b.Bitrate, listen)
	if useMQTT {
		env.say("  MQTT      %s:%d, topic %s", cfg.MQTT.Host, cfg.MQTT.Port, b.Device.Topic)
	} else {
		env.say("  MQTT      off")
	}
	env.say("  config    %s", path)
	if proto.Level < protocol.Verified {
		env.say("  note      %s", proto.Note)
	}
	ok, err := env.yesNo("\nProceed?", true)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrAborted
	}

	// apply the interface settings
	cmds := IfaceCommands(b.Interface, b.Bitrate, listen)
	env.say("\nInterface settings (need root):")
	for _, c := range cmds {
		env.say("  $ sudo %s", strings.Join(c, " "))
	}
	apply, err := env.yesNo("Apply them now?", true)
	if err != nil {
		return nil, err
	}
	if apply {
		for _, c := range cmds {
			if err := env.Run(c...); err != nil {
				return nil, fmt.Errorf("%s failed: %w", strings.Join(c, " "), err)
			}
		}
		env.say("Interface %s is up.", b.Interface)
	} else {
		env.say("Skipped. Run the commands above before starting the driver.")
	}

	// write the config
	data, err := json.MarshalIndent(&cfg, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := env.WriteFile(path, append(data, '\n')); err != nil {
		return nil, fmt.Errorf("write %s: %w", path, err)
	}
	env.say("Saved %s", path)

	res := &Result{ConfigPath: path, Link: "can", Interface: b.Interface, Protocol: proto.Name, MQTT: useMQTT}
	if res.Action, err = env.askNext(useMQTT); err != nil {
		return nil, err
	}
	return res, nil
}

// askMQTT asks whether to publish to MQTT and, if so, the broker settings.
func (e Env) askMQTT(cfg *config.Config) (bool, error) {
	use, err := e.yesNo("Publish to MQTT?", false)
	if err != nil || !use {
		return false, err
	}
	if cfg.MQTT.Host, err = e.ask("MQTT broker host", "localhost"); err != nil {
		return false, err
	}
	ps, err := e.ask("MQTT broker port", "1883")
	if err != nil {
		return false, err
	}
	if cfg.MQTT.Port, err = strconv.Atoi(ps); err != nil {
		return false, fmt.Errorf("invalid MQTT port %q", ps)
	}
	if cfg.MQTT.TopicPrefix, err = e.ask("Topic prefix", "lmh"); err != nil {
		return false, err
	}
	return true, nil
}

// askPath asks where to save the config and confirms an overwrite.
func (e Env) askPath() (string, error) {
	path, err := e.ask("\nSave config to", "./lmh-can.json")
	if err != nil {
		return "", err
	}
	if e.Exists != nil && e.Exists(path) {
		ok, err := e.yesNo(path+" exists. Overwrite?", false)
		if err != nil {
			return "", err
		}
		if !ok {
			return "", ErrAborted
		}
	}
	return path, nil
}

// askNext asks what to do once the config is saved.
func (e Env) askNext(useMQTT bool) (Action, error) {
	aitems := []item{{"live", "Show live data now (Ctrl+C to stop)"}}
	if useMQTT {
		aitems = append(aitems, item{"mqtt", "Run now and publish to MQTT"})
	}
	aitems = append(aitems, item{"save", "Done, just save"})
	ai, err := e.menu("What next?", aitems, 0)
	if err != nil {
		return ActionSave, err
	}
	switch aitems[ai].Key {
	case "live":
		return ActionLive, nil
	case "mqtt":
		return ActionService, nil
	}
	return ActionSave, nil
}

// runRS485 is the RS485 branch: protocol, port, baud, format, address, name, MQTT, save.
func runRS485(env Env, class string) (*Result, error) {
	// protocol
	type rsProto struct {
		Name         string
		Level        protocol.Level
		Note         string
		DefaultBaud  int
		DefaultSlave int
	}
	var plist []rsProto
	what, deviceLabel, defName := "BMS", "BMS (battery)", "bms1"
	if class == "inverter" {
		what, deviceLabel, defName = "inverter", "inverter", "inv1"
		for _, p := range rs485.AllInverters() {
			plist = append(plist, rsProto{p.Name, p.Level, p.Note, p.DefaultBaud, p.DefaultSlave})
		}
	} else {
		for _, p := range rs485.All() {
			plist = append(plist, rsProto{p.Name, p.Level, p.Note, p.DefaultBaud, p.DefaultSlave})
		}
	}
	var pitems []item
	for _, p := range plist {
		label := fmt.Sprintf("%-13s %s", p.Name, p.Level)
		if p.Level == protocol.Unsupported {
			label = fmt.Sprintf("%-13s not supported yet (needs a capture)", p.Name)
		}
		pitems = append(pitems, item{p.Name, label})
	}
	var proto rsProto
	for {
		q := "Which RS485 protocol is the BMS set to? (must match the BMS setting in PbmsTools)"
		if class == "inverter" {
			q = "Which RS485 protocol does the inverter speak?"
		}
		i, err := env.menu(q, pitems, 0)
		if err != nil {
			return nil, err
		}
		if plist[i].Level == protocol.Unsupported {
			env.say("%s is not supported yet: capture its traffic with `lmh-can sniff` and send the log. Pick another.", pitems[i].Key)
			continue
		}
		proto = plist[i]
		break
	}
	// serial port
	var ports []string
	if env.SerialPorts != nil {
		ports = env.SerialPorts()
	}
	port := ""
	env.say("\nOn the LMH tested, RS485-3 (A3 / B3 / GND3) is /dev/ttyACM1. Connect GND as well as A and B.")
	if len(ports) > 0 {
		pit := make([]item, 0, len(ports)+1)
		for _, p := range ports {
			pit = append(pit, item{p, p})
		}
		pit = append(pit, item{"other", "other (type a path)"})
		pi, err := env.menu("Serial port the "+what+" is wired to", pit, 0)
		if err != nil {
			return nil, err
		}
		if pi < len(ports) {
			port = ports[pi]
		}
	} else {
		env.say("No serial ports found (/dev/ttyACM*, /dev/ttyUSB*).")
	}
	var err error
	if port == "" {
		if port, err = env.ask("Serial port", "/dev/ttyACM1"); err != nil {
			return nil, err
		}
	}

	// baud
	bauds := []int{9600, 19200, 38400, 57600, 115200, 4800, 2400}
	bit := make([]item, 0, len(bauds)+1)
	def := 0
	for i, b := range bauds {
		label := strconv.Itoa(b)
		if b == proto.DefaultBaud {
			def = i
			label += "  (default for this protocol)"
		}
		bit = append(bit, item{strconv.Itoa(b), label})
	}
	bit = append(bit, item{"other", "other"})
	bi, err := env.menu("Baud rate", bit, def)
	if err != nil {
		return nil, err
	}
	baud := 0
	if bi < len(bauds) {
		baud = bauds[bi]
	}

	// format
	fmts := []struct {
		parity   string
		stopBits int
	}{{"N", 1}, {"E", 1}, {"O", 1}, {"N", 2}}
	fi, err := env.menu("Data format", []item{{"8N1", "8N1  (default)"}, {"8E1", "8E1"}, {"8O1", "8O1"}, {"8N2", "8N2"}}, 0)
	if err != nil {
		return nil, err
	}

	cfg := config.Config{RS485: []config.Serial{{Port: port, Baud: baud, Parity: fmts[fi].parity, StopBits: fmts[fi].stopBits}}}
	ser := &cfg.RS485[0]
	ser.Device.Protocol, ser.Device.Class = proto.Name, class

	// device address, name: re-asked until the whole config validates
	for {
		if ser.Baud == 0 {
			s, err := env.ask("Baud rate in bit/s", "9600")
			if err != nil {
				return nil, err
			}
			n, err := strconv.Atoi(s)
			if err != nil {
				env.say("Not a number: %q", s)
				continue
			}
			ser.Baud = n
		}
		sl, err := env.ask("\nDevice address (Modbus slave id or Pylon ADR, 1-247)", strconv.Itoa(proto.DefaultSlave))
		if err != nil {
			return nil, err
		}
		if ser.Slave, err = strconv.Atoi(sl); err != nil {
			env.say("Not a number: %q", sl)
			continue
		}
		if ser.Device.Name, err = env.ask("Name for this device (used in the MQTT topic)", defName); err != nil {
			return nil, err
		}
		trial := cfg
		trial.RS485 = append([]config.Serial(nil), cfg.RS485...)
		if err := trial.Validate(); err != nil {
			env.say("Problem: %v", err)
			if strings.Contains(err.Error(), "baud") {
				ser.Baud = 0
			}
			continue
		}
		break
	}

	useMQTT, err := env.askMQTT(&cfg)
	if err != nil {
		return nil, err
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	path, err := env.askPath()
	if err != nil {
		return nil, err
	}

	r := cfg.RS485[0]
	env.say("\nSummary")
	env.say("  device    %s over RS485, name %q", deviceLabel, r.Device.Name)
	env.say("  protocol  %s (%s)", proto.Name, proto.Level)
	env.say("  port      %s at %d baud, 8%s%d, address %d", r.Port, r.Baud, r.Parity, r.StopBits, r.Slave)
	if useMQTT {
		env.say("  MQTT      %s:%d, topic %s", cfg.MQTT.Host, cfg.MQTT.Port, r.Device.Topic)
	} else {
		env.say("  MQTT      off")
	}
	env.say("  config    %s", path)
	env.say("  note      %s", proto.Note)
	ok, err := env.yesNo("\nProceed?", true)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrAborted
	}

	data, err := json.MarshalIndent(&cfg, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := env.WriteFile(path, append(data, '\n')); err != nil {
		return nil, fmt.Errorf("write %s: %w", path, err)
	}
	env.say("Saved %s", path)

	res := &Result{ConfigPath: path, Link: "rs485", Class: class, Interface: r.Port, Protocol: proto.Name, MQTT: useMQTT,
		Baud: r.Baud, Parity: r.Parity, StopBits: r.StopBits, Slave: r.Slave}
	if res.Action, err = env.askNext(useMQTT); err != nil {
		return nil, err
	}
	return res, nil
}
