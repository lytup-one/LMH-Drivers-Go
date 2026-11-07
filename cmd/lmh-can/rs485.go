package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"math"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"lmhcan/internal/config"
	"lmhcan/internal/engine"
	"lmhcan/internal/model"
	"lmhcan/internal/protocol"
	"lmhcan/internal/publish"
	"lmhcan/internal/rs485"
	"lmhcan/internal/serialport"
)

// splitPort takes an optional leading PORT argument (not starting with "-").
func splitPort(args []string, def string) (string, []string) {
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		return args[0], args[1:]
	}
	return def, args
}

// openPort opens the serial port and returns it with a close func.
func openPort(path string, line serialport.Config) (serialport.Port, func(), error) {
	p, err := serialport.Open(path, line)
	if err != nil {
		return nil, nil, err
	}
	return p, func() { p.Close() }, nil
}

// cmdPoll is the RS485 bench tool: poll one BMS and print JSON.
//
//	lmh-can poll /dev/ttyACM1 -device battery|inverter -protocol NAME [-baud 9600] [-parity N] [-stop 1] [-slave 1] [-once] [-interval 1] [-stale 10]
func cmdPoll(args []string) int {
	port, args := splitPort(args, "/dev/ttyACM1")
	fs := flag.NewFlagSet("poll", flag.ExitOnError)
	name := fs.String("protocol", "", "RS485 protocol name of that device (see `lmh-can protocols`)")
	baud := fs.Int("baud", 0, "baud rate (default: the protocol's)")
	parity := fs.String("parity", "N", "parity N, E or O")
	stop := fs.Int("stop", 1, "stop bits 1 or 2")
	device := fs.String("device", "", "what is connected: battery or inverter (required)")
	slave := fs.Int("slave", 0, "device address: Modbus slave id or Pylon ADR (default: the protocol's)")
	once := fs.Bool("once", false, "read once, print indented JSON and exit")
	interval := fs.Float64("interval", 1, "poll period, seconds")
	stale := fs.Float64("stale", 10, "seconds without a good poll before stale=true")
	fs.Parse(args)
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	if err := rs485.CheckDevice(*device, *name); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	if strings.EqualFold(strings.TrimSpace(*device), "inverter") {
		return pollInverter(port, *name, *baud, *parity, *stop, *slave, *once, *interval, *stale, log)
	}
	info, err := rs485.Lookup(*name)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	if *baud == 0 {
		*baud = info.DefaultBaud
	}
	if *slave == 0 {
		*slave = info.DefaultSlave
	}
	if *slave < 1 || *slave > 247 {
		fmt.Fprintln(os.Stderr, "error: slave must be 1..247")
		return 1
	}
	line := serialport.Config{Baud: *baud, Parity: strings.ToUpper(*parity + "N")[0], StopBits: *stop}
	if err := line.Validate(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	cl, closePort, err := openPort(port, line)
	if err != nil {
		log.Error("open port", "port", port, "err", err)
		return 1
	}
	defer closePort()
	poller := info.New(cl, byte(*slave))
	log.Info("polling", "port", port, "protocol", info.Name, "baud", *baud, "format", fmt.Sprintf("8%c%d", line.Parity, line.StopBits), "slave", *slave)

	if *once {
		st := model.Battery{Device: port, Protocol: info.Name}
		var err error
		for try := 0; try < 3; try++ { // first request after opening the port sometimes gets no answer
			if err = poller.Poll(&st); err == nil {
				break
			}
		}
		if err != nil {
			log.Error("no valid reply", "err", err,
				"hint", "check A/B (swap them), GND, baud, slave address; see README, RS485 troubleshooting")
			return 1
		}
		st.TS = time.Now().Unix()
		if st.VoltageV != nil && st.CurrentA != nil {
			st.PowerW = model.F(math.Round(*st.VoltageV**st.CurrentA*10) / 10)
		}
		out := st.Normalized()
		js, _ := json.MarshalIndent(&out, "", "  ")
		fmt.Println(string(js))
		return 0
	}

	ctx, stop2 := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop2()
	dev := engine.Device{Name: port, Protocol: info.Name, Topic: "poll",
		Interval:   time.Duration(*interval * float64(time.Second)),
		StaleAfter: time.Duration(*stale * float64(time.Second))}
	log.Info("polling (Ctrl+C to stop)")
	if err := engine.RunPolled(ctx, poller.Poll, dev, &publish.JSONLines{W: os.Stdout}, log); err != nil {
		log.Error("stopped", "err", err)
		return 1
	}
	return 0
}

// pollInverter is cmdPoll for an inverter protocol.
func pollInverter(port, name string, baud int, parity string, stopBits, slave int, once bool, interval, stale float64, log *slog.Logger) int {
	info, _ := rs485.LookupInverter(name)
	if baud == 0 {
		baud = info.DefaultBaud
	}
	if slave == 0 {
		slave = info.DefaultSlave
	}
	if slave < 1 || slave > 247 {
		fmt.Fprintln(os.Stderr, "error: slave must be 1..247")
		return 1
	}
	line := serialport.Config{Baud: baud, Parity: strings.ToUpper(parity + "N")[0], StopBits: stopBits}
	if err := line.Validate(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	cl, closePort, err := openPort(port, line)
	if err != nil {
		log.Error("open port", "port", port, "err", err)
		return 1
	}
	defer closePort()
	poller := info.New(cl, byte(slave))
	log.Info("polling inverter", "port", port, "protocol", info.Name, "baud", baud, "format", fmt.Sprintf("8%c%d", line.Parity, line.StopBits), "slave", slave)
	if info.Level < protocol.Captured {
		log.Warn("protocol is not verified on real hardware", "protocol", info.Name, "level", info.Level.String())
	}
	if once {
		st := model.Inverter{Device: port, Protocol: info.Name}
		var err error
		for try := 0; try < 3; try++ {
			if err = poller.Poll(&st); err == nil {
				break
			}
		}
		if err != nil {
			log.Error("no valid reply", "err", err,
				"hint", "check A/B (swap them), GND, baud, slave address (inverter menu parameter 14); on SRNE HFP set RS485-2 to [32] SLA, not BMS; see README")
			return 1
		}
		st.TS = time.Now().Unix()
		st.Finish()
		js, _ := json.MarshalIndent(&st, "", "  ")
		fmt.Println(string(js))
		return 0
	}
	ctx, stop2 := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop2()
	dev := engine.Device{Name: port, Protocol: info.Name, Topic: "poll",
		Interval:   time.Duration(interval * float64(time.Second)),
		StaleAfter: time.Duration(stale * float64(time.Second))}
	src := &engine.InverterSource{PollFn: poller.Poll, State: model.Inverter{Device: port, Protocol: info.Name}}
	log.Info("polling (Ctrl+C to stop)")
	if err := engine.RunSource(ctx, src, dev, &publish.JSONLines{W: os.Stdout}, log); err != nil {
		log.Error("stopped", "err", err)
		return 1
	}
	return 0
}

// cmdSniff records raw RS485 traffic without ever transmitting: the way to
// capture an unsupported protocol (BMS <-> inverter) for a new decoder.
//
//	lmh-can sniff /dev/ttyACM1 [-baud 9600] [-parity N] [-stop 1] [-gap 15] [-seconds 60] [-out capture.txt]
func cmdSniff(args []string) int {
	port, args := splitPort(args, "/dev/ttyACM1")
	fs := flag.NewFlagSet("sniff", flag.ExitOnError)
	baud := fs.Int("baud", 9600, "baud rate")
	parity := fs.String("parity", "N", "parity N, E or O")
	stop := fs.Int("stop", 1, "stop bits 1 or 2")
	gap := fs.Int("gap", 15, "idle time in ms that ends a frame (lower if request and reply merge, raise if frames split)")
	seconds := fs.Int("seconds", 0, "stop after this many seconds (0 = until Ctrl+C)")
	outFile := fs.String("out", "", "also write the capture to this file")
	fs.Parse(args)
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	line := serialport.Config{Baud: *baud, Parity: strings.ToUpper(*parity + "N")[0], StopBits: *stop}
	if err := line.Validate(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	p, err := serialport.Open(port, line)
	if err != nil {
		log.Error("open port", "port", port, "err", err)
		return 1
	}
	defer p.Close()

	w := os.Stdout
	var mw *os.File = w
	if *outFile != "" {
		f, err := os.Create(*outFile)
		if err != nil {
			log.Error("create output", "err", err)
			return 1
		}
		defer f.Close()
		mw = f
	}
	ctx, stopSig := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stopSig()
	if *seconds > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(*seconds)*time.Second)
		defer cancel()
	}
	hdr := fmt.Sprintf("# lmh-can sniff %s %d 8%c%d gap %dms started %s", port, *baud, line.Parity, line.StopBits, *gap, time.Now().Format(time.RFC3339))
	fmt.Fprintln(w, hdr)
	if mw != w {
		fmt.Fprintln(mw, hdr)
	}
	log.Info("sniffing, receive only (Ctrl+C to stop)", "port", port)
	var dst = multi{w, mw}
	n, err := rs485.WriteSniff(ctx, p, time.Duration(*gap)*time.Millisecond, dst)
	log.Info("done", "chunks", n)
	if err != nil {
		log.Error("read failed", "err", err)
		return 1
	}
	if n == 0 {
		log.Warn("nothing received: check A/B (swap them), GND, baud/parity, and that something is talking on the bus")
	}
	return 0
}

// multi writes to stdout and, when different, a file.
type multi struct{ a, b *os.File }

func (m multi) Write(p []byte) (int, error) {
	n, err := m.a.Write(p)
	if m.b != m.a {
		m.b.Write(p)
	}
	return n, err
}

// startSerial launches the engine for one configured RS485 device.
func startSerial(ctx context.Context, stop context.CancelFunc, wg *sync.WaitGroup, failed chan<- struct{},
	log *slog.Logger, pub publish.Publisher, s config.Serial) bool {
	var src engine.Source
	var level protocol.Level
	var name string
	dev := engine.Device{Name: s.Device.Name, Topic: s.Device.Topic, Interval: s.Device.Interval(), StaleAfter: s.Device.StaleAfter()}
	cl, closePort, err := openPort(s.Port, s.Line())
	if err != nil {
		log.Error("open port", "port", s.Port, "err", err)
		return false
	}
	if s.Device.Class == "inverter" {
		info, _ := rs485.LookupInverter(s.Device.Protocol)
		level, name = info.Level, info.Name
		poller := info.New(cl, byte(s.Slave))
		src = &engine.InverterSource{PollFn: poller.Poll, State: model.Inverter{Device: dev.Name, Protocol: info.Name}}
	} else {
		info, _ := rs485.Lookup(s.Device.Protocol)
		level, name = info.Level, info.Name
		poller := info.New(cl, byte(s.Slave))
		src = engine.NewBatterySource(poller.Poll, model.Battery{Device: dev.Name, Protocol: info.Name})
	}
	dev.Protocol = name
	if level < protocol.Captured {
		log.Warn("protocol is not verified on real hardware", "device", dev.Name, "protocol", name, "level", level.String())
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer closePort()
		log.Info("polling", "port", s.Port, "device", dev.Name, "class", s.Device.Class, "protocol", dev.Protocol,
			"baud", s.Baud, "slave", s.Slave, "topic", dev.Topic)
		if err := engine.RunSource(ctx, src, dev, pub, log); err != nil {
			log.Error("port stopped", "port", s.Port, "err", err)
			failed <- struct{}{}
			stop()
		}
	}()
	return true
}
