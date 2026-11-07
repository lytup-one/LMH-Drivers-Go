//go:build linux

package canbus

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Link is a CAN network interface found on the system.
type Link struct {
	Name  string
	State string // operstate from sysfs, e.g. "up", "down", "unknown"
}

// ListCAN returns the CAN interfaces (ARPHRD_CAN = 280) in name order.
func ListCAN() []Link {
	matches, _ := filepath.Glob("/sys/class/net/*/type")
	var out []Link
	for _, m := range matches {
		raw, err := os.ReadFile(m)
		if err != nil || strings.TrimSpace(string(raw)) != "280" {
			continue
		}
		dir := filepath.Dir(m)
		state := "unknown"
		if s, err := os.ReadFile(filepath.Join(dir, "operstate")); err == nil {
			state = strings.TrimSpace(string(s))
		}
		out = append(out, Link{Name: filepath.Base(dir), State: state})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
