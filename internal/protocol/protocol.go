// Package protocol maps CAN frames to the normalized model. One Decoder per
// protocol; the registry records, honestly, how well each one is known.
package protocol

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"lmhcan/internal/canbus"
	"lmhcan/internal/model"
)

// Decoder turns frames into battery state. Decode returns true when the frame
// was recognised and applied. A Decoder may keep state between frames, so
// create one per device with Info.New.
type Decoder interface {
	Decode(f canbus.Frame, b *model.Battery) bool
}

// Level says how far a protocol has been checked.
type Level int

const (
	// Unsupported: no frame specification or capture yet. Selecting it is an
	// error; we do not guess a layout.
	Unsupported Level = iota
	// Unverified: implemented from documentation or old code, never checked
	// against real frames.
	Unverified
	// Captured: derived from, and tested against, a capture of the real BMS
	// in this mode. Values were cross-checked against the battery's own
	// readings, but no vendor specification was available and no live
	// comparison with an inverter of that brand has been made. Fields that
	// could not be identified are not decoded.
	Captured
	// Verified: additionally compared live against PbmsTools on the LMH.
	Verified
)

func (l Level) String() string {
	switch l {
	case Verified:
		return "verified"
	case Captured:
		return "captured"
	case Unverified:
		return "unverified"
	default:
		return "unsupported"
	}
}

// Info describes one registered protocol.
type Info struct {
	Name  string
	Level Level
	Note  string
	New   func() Decoder // nil when Unsupported
}

// ErrNotImplemented is returned (wrapped) for Unsupported protocols.
var ErrNotImplemented = errors.New("protocol not implemented")

const needSpec = "no capture or frame specification yet. To add it: select this protocol on the BMS in PbmsTools, " +
	"record a few minutes with `candump -L can1 > capture.log`, or supply the vendor's CAN document; " +
	"then check the capture with `lmh-can replay`"

func dl(t table) func() Decoder { return func() Decoder { return dialect{t} } }

func entry(name string, lvl Level, note string, nw func() Decoder) Info {
	return Info{Name: name, Level: lvl, Note: note, New: nw}
}

const capNote = "from a capture of the BMS in this mode, cross-checked with the battery's other readings; "

// The order is the order of the supported-protocol list.
var all = []Info{
	entry("pylontech", Verified, "frames 0x351, 0x355, 0x356, 0x359, 0x35C, 0x35E; protection/alarm/request bytes reported raw", dl(tPylon())),
	entry("goodwe", Captured, capNote+"adds 0x370 (cell and temperature extremes); 0x351 reports no discharge voltage limit", dl(merge(tPylon(), table{0x370: h370GoodweStuder}))),
	entry("growatt", Captured, capNote+"0x311-0x319, 0x322 incl. all 16 cell voltages and cycle count; 0x312, 0x320, 0x323 not decoded", newGrowatt),
	entry("studer", Captured, capNote+"this BMS sends Goodwe-style frames in Studer mode (not the 0xB0/0xC0 frames of the old tool)", dl(merge(tPylon(), table{0x370: h370GoodweStuder}))),
	entry("srd", Captured, capNote+"no SOH reported in this mode; alarms raw (0x35A)", dl(tSMAFamily(false))),
	entry("victron", Captured, capNote+"0x373 cell/temperature extremes (whole kelvin), 0x379 installed Ah; alarms raw (0x35A)", dl(merge(tSMAFamily(true), table{0x373: h373, 0x379: h379}))),
	entry("victron23", Captured, capNote+"as victron; also sends ID strings and pack serial pieces (0x37x, 0x380-0x381), not decoded", dl(merge(tSMAFamily(true), table{0x373: h373, 0x379: h379}))),
	entry("luxpower", Captured, capNote+"cell extremes in 0x355, cycle count in 0x35C, installed Ah in 0x359", dl(merge(tCore(), table{0x355: h355Cells, 0x359: h359Lux, 0x35C: h35CCycles}))),
	entry("schneider", Captured, capNote+"29-bit IDs 0x320-0x333, 32-bit values; flags (0x320, 0x325) not decoded", newSchneider),
	entry("sma", Captured, capNote+"SOC in 0.01 % (0x355), installed Ah (0x35F); alarms raw (0x35A)", dl(tSMAFamily(true))),
	entry("sofar", Captured, capNote+"alarms raw (0x35A); 0x30F not decoded", dl(tSMAFamily(false))),
	entry("pv", Captured, capNote+"alarms raw (0x35A); 0x30F not decoded", dl(tSMAFamily(false))),
	entry("jinlang", Captured, capNote+"Pylontech-style frames; 0x71C not decoded", dl(tPylon())),
	entry("senergy", Captured, capNote+"adds 0x373 cell/temperature extremes and 0x379 installed Ah", dl(merge(tPylon(), table{0x373: h373, 0x379: h379}))),
	entry("tbb", Captured, capNote+"Pylontech-style frames", dl(tPylon())),
	entry("megarevo", Captured, capNote+"adds 0x373 cell/temperature extremes and 0x379 installed Ah", dl(merge(tPylon(), table{0x373: h373, 0x379: h379}))),
	// No capture available (not offered as a CAN protocol by this BMS):
	entry("deye", Unsupported, needSpec, nil),
	entry("gigacity", Unsupported, needSpec, nil),
	entry("afore", Unsupported, needSpec, nil),
	entry("sorotec", Unsupported, needSpec, nil),
	entry("must", Unsupported, needSpec, nil),
	entry("solis", Unsupported, needSpec, nil),
}

var aliases = map[string]string{
	"pylon": "pylontech", "jin_lang": "jinlang", "jin lang": "jinlang",
	"senergyinv": "senergy", "tbb_lithium": "tbb",
}

var registry = func() map[string]Info {
	m := map[string]Info{}
	for _, i := range all {
		m[i.Name] = i
	}
	return m
}()

// Lookup finds a protocol by name (case-insensitive, aliases accepted).
func Lookup(name string) (Info, error) {
	n := strings.ToLower(strings.TrimSpace(name))
	if a, ok := aliases[n]; ok {
		n = a
	}
	info, ok := registry[n]
	if !ok {
		names := make([]string, 0, len(all))
		for _, i := range all {
			names = append(names, i.Name)
		}
		sort.Strings(names)
		return Info{}, fmt.Errorf("unknown protocol %q (known: %s)", name, strings.Join(names, ", "))
	}
	if info.Level == Unsupported {
		return info, fmt.Errorf("%w: %s: %s", ErrNotImplemented, info.Name, info.Note)
	}
	return info, nil
}

// All returns every registered protocol in display order.
func All() []Info { return append([]Info(nil), all...) }
