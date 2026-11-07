//go:build !linux

package canbus

import "errors"

type IfInfo struct {
	Up         bool
	Bitrate    int
	ListenOnly bool
	State      string
}

func CheckInterface(name string) (IfInfo, error) {
	return IfInfo{}, errors.New("interface check is only available on Linux")
}
