// lmh-can: CAN and RS485 battery monitor for the LMH edge device.
//
//	lmh-can setup                                     interactive first-time setup (device, link, protocol, bitrate / baud, interface / port)
//	lmh-can listen can1                               CAN: print the state as JSON lines (no config, no MQTT)
//	lmh-can poll /dev/ttyACM1 -device battery -protocol pacemodbus    RS485: poll a device and print JSON (-device battery|inverter; add -once for a single read)
//	lmh-can sniff /dev/ttyACM1 -baud 9600             RS485: record raw traffic (receive only) to capture a new protocol
//	lmh-can probe /dev/ttyACM1 -device battery -style pylon    RS485: send read-only requests (battery styles: pylon, voltronic, luxpower; inverter: srne)
//	lmh-can run -config /etc/lmh-can/config.json     run the daemon
//	lmh-can replay -protocol pylontech -file x.log   decode a `candump -L` capture offline
//	lmh-can protocols                                 list protocols and how well each is known
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"lmhcan/internal/canbus"
	"lmhcan/internal/config"
	"lmhcan/internal/engine"
	"lmhcan/internal/model"
	"lmhcan/internal/protocol"
	"lmhcan/internal/publish"
	"lmhcan/internal/rs485"
)

var version = "dev"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "run":
		os.Exit(cmdRun(os.Args[2:]))
	case "setup":
		os.Exit(cmdSetup())
	case "listen":
		os.Exit(cmdListen(os.Args[2:]))
	case "poll":
		os.Exit(cmdPoll(os.Args[2:]))
	case "sniff":
		os.Exit(cmdSniff(os.Args[2:]))
	case "probe":
		os.Exit(cmdProbe(os.Args[2:]))
	case "replay":
		os.Exit(cmdReplay(os.Args[2:]))
	case "protocols":
		os.Exit(cmdProtocols())
	case "version":
		fmt.Println(version)
	default:
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: lmh-can setup | listen [IFACE] | poll [PORT] | sniff [PORT] | probe [PORT] | run -config FILE [-stdout] | replay -protocol NAME [-file FILE] | protocols | version")
}

func cmdProtocols() int {
	fmt.Println("CAN (lmh-can listen / run):")
	for _, p := range protocol.All() {
		note := p.Note
		if p.Level == protocol.Unsupported {
			note = "not decoded yet: needs a capture (candump -L) or the vendor's CAN document"
		}
		fmt.Printf("  %-13s %-11s %s\n", p.Name, p.Level, note)
	}
	fmt.Println("\nRS485 (lmh-can poll / run):")
	for _, p := range rs485.All() {
		note := p.Note
		if p.Level == protocol.Unsupported {
			note = "not decoded yet: needs a capture (lmh-can sniff) or the vendor's protocol document"
		}
		fmt.Printf("  %-13s %-11s %s\n", p.Name, p.Level, note)
	}
	fmt.Println("\nRS485 inverters (lmh-can poll / run, class \"inverter\"):")
	for _, p := range rs485.AllInverters() {
		fmt.Printf("  %-13s %-11s %s\n", p.Name, p.Level, p.Note)
	}
	return 0
}

func cmdRun(args []string) int {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	path := fs.String("config", "/etc/lmh-can/config.json", "config file")
	stdout := fs.Bool("stdout", false, "print messages instead of publishing to MQTT")
	fs.Parse(args)
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	cfg, err := config.Load(*path)
	if err != nil {
		log.Error("config", "err", err)
		return 1
	}

	// Check every interface before starting anything.
	for _, b := range cfg.Buses {
		info, err := canbus.CheckInterface(b.Interface)
		switch {
		case err != nil:
			log.Warn("could not verify interface settings", "interface", b.Interface, "err", err)
		case !info.Up:
			log.Error("interface is down; bring it up first", "interface", b.Interface,
				"hint", fmt.Sprintf("sudo ip link set %s type can bitrate %d && sudo ip link set %s up", b.Interface, b.Bitrate, b.Interface))
			return 1
		case info.Bitrate != 0 && info.Bitrate != b.Bitrate:
			log.Error("interface bitrate differs from config", "interface", b.Interface, "kernel", info.Bitrate, "config", b.Bitrate)
			return 1
		default:
			log.Info("interface ok", "interface", b.Interface, "bitrate", info.Bitrate, "state", info.State, "listen_only", info.ListenOnly)
		}
		if p, _ := protocol.Lookup(b.Device.Protocol); p.Level < protocol.Captured {
			log.Warn("protocol is not verified on real hardware", "device", b.Device.Name, "protocol", p.Name, "level", p.Level.String())
		}
	}

	var pub publish.Publisher
	if *stdout {
		pub = &publish.Stdout{W: os.Stdout}
	} else {
		svc := cfg.MQTT.TopicPrefix + "/lmh-can/status"
		pub = publish.NewMQTT(publish.MQTTConfig{
			Host: cfg.MQTT.Host, Port: cfg.MQTT.Port, ClientID: cfg.MQTT.ClientID,
			Username: cfg.MQTT.Username, Password: cfg.MQTT.Password,
			WillTopic: svc, WillPayload: "offline",
			OnConnect: func(m *publish.MQTT) { m.Publish(svc, []byte("online"), true) },
		}, log)
	}
	defer pub.Close()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	var wg sync.WaitGroup
	failed := make(chan struct{}, len(cfg.Buses)+len(cfg.RS485))
	for _, b := range cfg.Buses {
		bus, err := canbus.OpenSocketCAN(b.Interface)
		if err != nil {
			log.Error("open bus", "interface", b.Interface, "err", err)
			return 1
		}
		info, _ := protocol.Lookup(b.Device.Protocol)
		dev := engine.Device{Name: b.Device.Name, Protocol: info.Name, Topic: b.Device.Topic,
			Interval: b.Device.Interval(), StaleAfter: b.Device.StaleAfter()}
		wg.Add(1)
		go func(b config.Bus) {
			defer wg.Done()
			defer bus.Close()
			log.Info("monitoring", "interface", b.Interface, "device", dev.Name, "protocol", dev.Protocol, "topic", dev.Topic)
			if err := engine.Run(ctx, bus, info.New(), dev, pub, log); err != nil {
				log.Error("bus stopped", "interface", b.Interface, "err", err)
				failed <- struct{}{}
				stop()
			}
		}(b)
	}
	for _, r := range cfg.RS485 {
		if !startSerial(ctx, stop, &wg, failed, log, pub, r) {
			stop()
			wg.Wait()
			return 1
		}
	}
	wg.Wait()
	if len(failed) > 0 {
		return 1
	}
	return 0
}

// cmdReplay decodes a candump capture offline: the way to check a protocol
// against real frames before trusting it, and to see which IDs a decoder
// does not understand yet.
func cmdReplay(args []string) int {
	fs := flag.NewFlagSet("replay", flag.ExitOnError)
	name := fs.String("protocol", "pylontech", "protocol name")
	file := fs.String("file", "-", "candump log (-L or plain format); - = stdin")
	fs.Parse(args)

	info, err := protocol.Lookup(*name)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	if info.Level < protocol.Verified {
		fmt.Fprintf(os.Stderr, "note: %s decoder is %s\n", info.Name, info.Level)
	}
	in := os.Stdin
	if *file != "-" {
		if in, err = os.Open(*file); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			return 1
		}
		defer in.Close()
	}
	dec := info.New()
	state := model.Battery{Device: "replay", Protocol: info.Name}
	seen, decoded := map[uint32]int{}, map[uint32]int{}
	total := 0
	sc := bufio.NewScanner(in)
	for sc.Scan() {
		f, ok := canbus.ParseCandumpLine(sc.Text())
		if !ok {
			continue
		}
		total++
		seen[f.ID]++
		if dec.Decode(f, &state) {
			decoded[f.ID]++
		}
	}
	out, _ := json.MarshalIndent(&state, "", "  ")
	fmt.Println(string(out))
	fmt.Printf("\n%d frames\n", total)
	ids := make([]int, 0, len(seen))
	for id := range seen {
		ids = append(ids, int(id))
	}
	sort.Ints(ids)
	for _, id := range ids {
		mark := "decoded"
		if decoded[uint32(id)] == 0 {
			mark = "NOT decoded"
		}
		fmt.Printf("  0x%03X  %5d frames  %s\n", id, seen[uint32(id)], mark)
	}
	return 0
}

// cmdListen is the quick bench tool: decode one interface and print JSON lines.
//
//	lmh-can listen can1 [-protocol pylontech] [-interval 1] [-stale 10]
func cmdListen(args []string) int {
	iface := "can1"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		iface, args = args[0], args[1:]
	}
	fs := flag.NewFlagSet("listen", flag.ExitOnError)
	name := fs.String("protocol", "pylontech", "protocol name")
	interval := fs.Float64("interval", 1, "print period, seconds")
	stale := fs.Float64("stale", 10, "seconds without a decoded frame before stale=true")
	fs.Parse(args)
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	info, err := protocol.Lookup(*name)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	if info.Level < protocol.Captured {
		log.Warn("protocol is not verified on real hardware", "protocol", info.Name, "level", info.Level.String())
	}
	if ii, err := canbus.CheckInterface(iface); err != nil {
		log.Warn("could not verify interface settings", "interface", iface, "err", err)
	} else if !ii.Up {
		log.Error("interface is down", "interface", iface,
			"hint", "sudo ip link set "+iface+" type can bitrate 500000 listen-only on && sudo ip link set "+iface+" up")
		return 1
	} else {
		log.Info("interface ok", "interface", iface, "bitrate", ii.Bitrate, "state", ii.State, "listen_only", ii.ListenOnly)
	}
	bus, err := canbus.OpenSocketCAN(iface)
	if err != nil {
		log.Error("open bus", "interface", iface, "err", err)
		return 1
	}
	defer bus.Close()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	dev := engine.Device{Name: iface, Protocol: info.Name, Topic: "listen",
		Interval:   time.Duration(*interval * float64(time.Second)),
		StaleAfter: time.Duration(*stale * float64(time.Second))}
	log.Info("listening (Ctrl+C to stop)", "interface", iface, "protocol", info.Name)
	if err := engine.Run(ctx, bus, info.New(), dev, &publish.JSONLines{W: os.Stdout}, log); err != nil {
		log.Error("bus stopped", "err", err)
		return 1
	}
	return 0
}
