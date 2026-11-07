//go:build linux

package canbus

import (
	"encoding/json"
	"fmt"
	"os/exec"
)

// IfInfo describes the kernel-side configuration of a CAN interface.
type IfInfo struct {
	Up         bool
	Bitrate    int  // 0 if it could not be read
	ListenOnly bool // only meaningful when ctrlmode could be read
	State      string
}

// CheckInterface reads the interface configuration with `ip -details -json`.
// The caller should treat an error as "could not verify", not as a failure.
func CheckInterface(name string) (IfInfo, error) {
	out, err := exec.Command("ip", "-details", "-json", "link", "show", "dev", name).Output()
	if err != nil {
		return IfInfo{}, fmt.Errorf("ip link show %s: %w", name, err)
	}
	var links []struct {
		Flags    []string `json:"flags"`
		LinkInfo struct {
			InfoData struct {
				State      string      `json:"state"`
				Ctrlmode   interface{} `json:"ctrlmode"`
				BitTimings struct {
					Bitrate int `json:"bitrate"`
				} `json:"bittiming"`
			} `json:"info_data"`
		} `json:"linkinfo"`
	}
	if err := json.Unmarshal(out, &links); err != nil || len(links) == 0 {
		return IfInfo{}, fmt.Errorf("cannot parse ip output for %s", name)
	}
	l := links[0]
	info := IfInfo{
		State:   l.LinkInfo.InfoData.State,
		Bitrate: l.LinkInfo.InfoData.BitTimings.Bitrate,
	}
	for _, f := range l.Flags {
		if f == "UP" {
			info.Up = true
		}
	}
	if list, ok := l.LinkInfo.InfoData.Ctrlmode.([]interface{}); ok {
		for _, v := range list {
			if s, ok := v.(string); ok && s == "LISTEN-ONLY" {
				info.ListenOnly = true
			}
		}
	}
	return info, nil
}
