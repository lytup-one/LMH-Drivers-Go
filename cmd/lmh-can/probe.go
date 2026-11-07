package main

import (
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"lmhcan/internal/modbus"
	"lmhcan/internal/rs485"
	"lmhcan/internal/serialport"
)

// cmdProbe sends read-only requests to a BMS and prints what it answers.
// It is for capturing a polled RS485 protocol when no inverter is connected.
//
//	lmh-can probe /dev/ttyUSB4 -device battery -style pylon [-adr 2,1] [-ver 20] [-baud 9600] [-out pylon_probe.txt]
//	lmh-can probe /dev/ttyUSB4 -device battery -style voltronic       Voltronic read requests
//	lmh-can probe /dev/ttyUSB4 -device inverter -style srne           SRNE inverter read requests
//	lmh-can probe /dev/ttyUSB4 -ascii '~20024642E00202FD33\r'      one raw request
//	lmh-can probe /dev/ttyUSB4 -hex '01 03 00 00 00 0a c5 cd'       one raw request
//	lmh-can probe /dev/ttyUSB4 -modbus -slave 1 -start 0 -count 110 dump Modbus holding registers (function 3)
func cmdProbe(args []string) int {
	port, args := splitPort(args, "/dev/ttyACM1")
	fs := flag.NewFlagSet("probe", flag.ExitOnError)
	device := fs.String("device", "", "what is connected: battery or inverter (required with -style)")
	style := fs.String("style", "", "request set to send. battery: pylon, voltronic, luxpower; inverter: srne")
	adrs := fs.String("adr", "2,1", "device addresses to try, comma separated (decimal)")
	ver := fs.String("ver", "20", "Pylon VER byte, hex")
	baud := fs.Int("baud", 9600, "baud rate")
	parity := fs.String("parity", "N", "parity N, E or O")
	stop := fs.Int("stop", 1, "stop bits 1 or 2")
	timeout := fs.Int("timeout", 1000, "ms to wait for a reply")
	ascii := fs.String("ascii", "", `send one raw request, with \r \n \xNN escapes`)
	hx := fs.String("hex", "", "send one raw request, hex bytes")
	outFile := fs.String("out", "", "also write the capture to this file")
	mb := fs.Bool("modbus", false, "dump Modbus holding registers (function 3) instead of sending a request set")
	slave := fs.Int("slave", 1, "Modbus slave address for -modbus")
	start := fs.Int("start", 0, "first register address for -modbus (0-based request address; a table's register N is address N-1)")
	count := fs.Int("count", 110, "number of registers for -modbus")
	chunk := fs.Int("chunk", 40, "registers per request for -modbus")
	fs.Parse(args)
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	if *style != "" {
		styles := map[string]string{"pylon": "battery", "voltronic": "battery", "luxpower": "battery", "srne": "inverter"}
		want, known := styles[*style]
		switch {
		case !known:
			fmt.Fprintf(os.Stderr, "error: unknown -style %q (battery: pylon, voltronic, luxpower; inverter: srne)\n", *style)
			return 1
		case *device == "":
			fmt.Fprintf(os.Stderr, "error: -device is required with -style: %s is a %s style: add -device %s\n", *style, want, want)
			return 1
		case !strings.EqualFold(*device, want):
			fmt.Fprintf(os.Stderr, "error: -style %s belongs to -device %s, not %s\n", *style, want, *device)
			return 1
		}
	}
	var requests []probeReq
	switch {
	case *mb:
		if *slave < 1 || *slave > 247 || *start < 0 || *count < 1 || *start+*count > 65536 || *chunk < 1 || *chunk > 125 {
			fmt.Fprintln(os.Stderr, "error: need slave 1..247, start >= 0, count >= 1, chunk 1..125")
			return 1
		}
	case *ascii != "":
		b, err := strconv.Unquote(`"` + strings.ReplaceAll(*ascii, `"`, `\"`) + `"`)
		if err != nil {
			fmt.Fprintln(os.Stderr, "error: -ascii:", err)
			return 1
		}
		requests = append(requests, probeReq{"raw", []byte(b), rs485.PylonComplete})
	case *hx != "":
		b, err := hex.DecodeString(strings.Join(strings.Fields(*hx), ""))
		if err != nil {
			fmt.Fprintln(os.Stderr, "error: -hex:", err)
			return 1
		}
		requests = append(requests, probeReq{"raw", b, nil})
	case *style == "pylon":
		v, err := strconv.ParseUint(*ver, 16, 8)
		if err != nil {
			fmt.Fprintln(os.Stderr, "error: -ver must be a hex byte, e.g. 20")
			return 1
		}
		for _, a := range strings.Split(*adrs, ",") {
			n, err := strconv.Atoi(strings.TrimSpace(a))
			if err != nil || n < 0 || n > 255 {
				fmt.Fprintf(os.Stderr, "error: bad address %q\n", a)
				return 1
			}
			for _, r := range rs485.PylonProbeRequests {
				requests = append(requests, probeReq{fmt.Sprintf("%s (adr %d)", r.Name, n),
					rs485.BuildPylonProbe(r, byte(v), byte(n)), rs485.PylonComplete})
			}
		}
	case *style == "voltronic":
		if *slave < 1 || *slave > 255 {
			fmt.Fprintln(os.Stderr, "error: need -slave 1..255 (1 is the common address)")
			return 1
		}
		for _, r := range rs485.VoltronicProbeRequests {
			requests = append(requests, probeReq{fmt.Sprintf("%s (0x%04X, %d words)", r.Name, r.Start, r.Words),
				rs485.BuildVoltronicRead(byte(*slave), r.Start, r.Words), rs485.VoltronicComplete})
		}
	case *style == "luxpower":
		if *slave < 1 || *slave > 255 {
			fmt.Fprintln(os.Stderr, "error: need -slave 1..255 (the document says 1)")
			return 1
		}
		requests = append(requests,
			probeReq{"luxpower: 16 registers from 0, little-endian request (as in the document)", rs485.BuildLuxpowerRead(byte(*slave), 0, 16), rs485.LuxpowerComplete},
			probeReq{"standard Modbus: 16 registers from 0, big-endian request (for comparison)", modbusRead(byte(*slave), 0, 16), nil})
	case *style == "srne":
		if *slave < 1 || *slave > 247 {
			fmt.Fprintln(os.Stderr, "error: need -slave 1..247 (the inverter's RS485 address, menu parameter 14; default 1)")
			return 1
		}
		for _, r := range []struct {
			name         string
			start, count uint16
		}{{"controller data: battery, PV, charge", 0x100, 18}, {"inverter data: faults, state, grid, output, load", 0x200, 32},
			{"heat sink temperatures, PV charge current", 0x220, 6}, {"versions", 0x14, 4}, {"address, model, protocol version", 0x1A, 4},
			{"serial number string", 0x35, 20}, {"energy counters (today)", 0xF02D, 19}, {"energy counters (totals)", 0xF046, 6}} {
			requests = append(requests, probeReq{fmt.Sprintf("%s (0x%04X, %d registers)", r.name, r.start, r.count),
				modbusRead(byte(*slave), r.start, r.count), rs485.ModbusComplete})
		}
	default:
		fmt.Fprintln(os.Stderr, "error: give -style pylon, -style voltronic, -style luxpower, -style srne, or one of -ascii / -hex")
		return 1
	}

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

	var w io.Writer = os.Stdout
	if *outFile != "" {
		f, err := os.Create(*outFile)
		if err != nil {
			log.Error("create output", "err", err)
			return 1
		}
		defer f.Close()
		w = io.MultiWriter(os.Stdout, f)
	}
	fmt.Fprintf(w, "# lmh-can probe %s %d 8%c%d started %s\n", port, *baud, line.Parity, line.StopBits, time.Now().Format(time.RFC3339))

	if *mb {
		return probeModbus(w, p, byte(*slave), *start, *count, *chunk, time.Duration(*timeout)*time.Millisecond, log)
	}

	answered := 0
	for _, r := range requests {
		fmt.Fprintf(w, "\n## %s\n", r.name)
		fmt.Fprintln(w, "TX "+show(r.data))
		rsp, err := rs485.Exchange(p, r.data, time.Duration(*timeout)*time.Millisecond, 100*time.Millisecond, r.complete)
		if err != nil {
			log.Error("exchange failed", "err", err)
			return 1
		}
		if len(rsp) == 0 {
			fmt.Fprintln(w, "RX (no reply)")
			continue
		}
		answered++
		fmt.Fprintln(w, "RX "+show(rsp))
		if f, err := rs485.DecodePylon(rsp); err == nil {
			fmt.Fprintf(w, "   pylon frame: ver=%02X adr=%02X cid1=%02X cid2/rtn=%02X info=%d bytes: % X\n", f.Ver, f.Adr, f.CID1, f.CID2, len(f.Info), f.Info)
		} else if *style == "pylon" {
			fmt.Fprintf(w, "   not a valid Pylon frame: %v\n", err)
		}
	}
	fmt.Fprintf(w, "\n# %d of %d requests answered\n", answered, len(requests))
	if answered == 0 {
		log.Warn("no reply to any request: check the BMS protocol setting in PbmsTools, A/B (swap them), GND, baud rate, or try -adr 0,1,2,3")
	}
	return 0
}

type probeReq struct {
	name     string
	data     []byte
	complete func([]byte) bool
}

// show renders bytes as hex plus printable ASCII.
func show(b []byte) string {
	h := make([]string, len(b))
	a := make([]byte, len(b))
	for i, c := range b {
		h[i] = fmt.Sprintf("%02x", c)
		a[i] = '.'
		if c >= 0x20 && c < 0x7f {
			a[i] = c
		}
	}
	return fmt.Sprintf("%3d B  %s  |%s", len(b), strings.Join(h, " "), a)
}

// probeModbus prints holding registers as decimal, hex and signed values. A
// chunk the device rejects with an exception is retried one register at a
// time, which shows where the holes in its map are.
func probeModbus(w io.Writer, p serialport.Port, slave byte, start, count, chunk int, timeout time.Duration, log *slog.Logger) int {
	cl := &modbus.Client{P: p, Timeout: timeout}
	fmt.Fprintf(w, "# modbus slave %d, addresses %d..%d (address = register number in a 1-based table minus 1)\n", slave, start, start+count-1)
	fmt.Fprintln(w, "# addr   dec    hex   signed")
	got, failed := 0, 0
	print := func(addr int, v uint16) {
		fmt.Fprintf(w, "%6d %6d  0x%04X %6d\n", addr, v, v, int16(v))
		got++
	}
	for a := start; a < start+count; a += chunk {
		n := chunk
		if a+n > start+count {
			n = start + count - a
		}
		regs, err := cl.ReadHolding(slave, uint16(a), uint16(n))
		if err == nil {
			for i, v := range regs {
				print(a+i, v)
			}
			continue
		}
		var ex *modbus.Exception
		if !errors.As(err, &ex) {
			fmt.Fprintf(w, "# addresses %d..%d: %v\n", a, a+n-1, err)
			failed += n
			continue
		}
		fmt.Fprintf(w, "# addresses %d..%d rejected (%v); reading one by one\n", a, a+n-1, err)
		for i := 0; i < n; i++ {
			regs, err := cl.ReadHolding(slave, uint16(a+i), 1)
			if err != nil {
				fmt.Fprintf(w, "%6d  --  %v\n", a+i, err)
				failed++
				continue
			}
			print(a+i, regs[0])
		}
	}
	fmt.Fprintf(w, "\n# %d registers read, %d failed\n", got, failed)
	if got == 0 {
		log.Warn("no register could be read: check slave address, baud rate and parity, A/B (swap them), GND, and the protocol set on the BMS")
		return 1
	}
	return 0
}

// modbusRead builds a standard function-3 request.
func modbusRead(slave byte, start, count uint16) []byte {
	req := []byte{slave, 0x03, byte(start >> 8), byte(start), byte(count >> 8), byte(count)}
	crc := modbus.CRC16(req)
	return append(req, byte(crc), byte(crc>>8))
}
