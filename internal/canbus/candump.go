package canbus

import (
	"encoding/hex"
	"strconv"
	"strings"
	"time"
)

// ParseCandumpLine parses one line of `candump -L` output, e.g.
//
//	(1793290950.196751) can1 359#0200000001704E00
//
// and also the plain `candump` format:
//
//	can1  359   [8]  02 00 00 00 01 70 4E 00
//
// It returns false for lines that are not classic data frames (CAN FD, remote
// frames, comments, blanks).
func ParseCandumpLine(line string) (Frame, bool) {
	fields := strings.Fields(line)
	if len(fields) < 3 {
		return Frame{}, false
	}
	var f Frame
	// -L format: (time) iface ID#DATA
	if strings.HasPrefix(fields[0], "(") && strings.HasSuffix(fields[0], ")") {
		ts, err := strconv.ParseFloat(strings.Trim(fields[0], "()"), 64)
		if err == nil {
			sec := int64(ts)
			f.Time = time.Unix(sec, int64((ts-float64(sec))*1e9))
		}
		idData := fields[2]
		if strings.Contains(idData, "##") {
			return Frame{}, false // CAN FD
		}
		parts := strings.SplitN(idData, "#", 2)
		if len(parts) != 2 || strings.HasPrefix(parts[1], "R") {
			return Frame{}, false
		}
		id, err := strconv.ParseUint(parts[0], 16, 32)
		if err != nil {
			return Frame{}, false
		}
		data, err := hex.DecodeString(parts[1])
		if err != nil || len(data) > 8 {
			return Frame{}, false
		}
		f.ID, f.Extended, f.Data = uint32(id), len(parts[0]) > 3, data
		return f, true
	}
	// plain format: iface ID [n] b0 b1 ...
	if len(fields) >= 3 && strings.HasPrefix(fields[2], "[") {
		id, err := strconv.ParseUint(fields[1], 16, 32)
		if err != nil {
			return Frame{}, false
		}
		var data []byte
		for _, s := range fields[3:] {
			b, err := strconv.ParseUint(s, 16, 8)
			if err != nil {
				return Frame{}, false
			}
			data = append(data, byte(b))
		}
		f.ID, f.Extended, f.Data = uint32(id), len(fields[1]) > 3, data
		return f, true
	}
	return Frame{}, false
}
