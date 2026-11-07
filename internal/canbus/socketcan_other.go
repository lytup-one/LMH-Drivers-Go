//go:build !linux

package canbus

import "errors"

// OpenSocketCAN is only available on Linux (SocketCAN).
func OpenSocketCAN(ifname string) (Bus, error) {
	return nil, errors.New("SocketCAN is only available on Linux")
}
