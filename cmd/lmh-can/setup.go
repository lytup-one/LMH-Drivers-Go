package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"lmhcan/internal/canbus"
	"lmhcan/internal/serialport"
	"lmhcan/internal/wizard"
)

// cmdSetup runs the interactive first-time setup (internal/wizard).
func cmdSetup() int {
	env := wizard.Env{
		In:  bufio.NewReader(os.Stdin),
		Out: os.Stdout,
		CANInterfaces: func() []wizard.Iface {
			var out []wizard.Iface
			for _, l := range canbus.ListCAN() {
				out = append(out, wizard.Iface{Name: l.Name, State: l.State})
			}
			return out
		},
		SerialPorts: serialport.List,
		Run: func(args ...string) error {
			if os.Geteuid() != 0 {
				args = append([]string{"sudo"}, args...)
			}
			c := exec.Command(args[0], args[1:]...)
			c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
			return c.Run()
		},
		WriteFile: func(path string, data []byte) error { return os.WriteFile(path, data, 0o640) },
		Exists: func(path string) bool {
			_, err := os.Stat(path)
			return err == nil
		},
	}
	res, err := wizard.Run(env)
	if err != nil {
		fmt.Fprintln(os.Stderr, "\n"+err.Error())
		return 1
	}
	live := []string{res.Interface, "-protocol", res.Protocol}
	liveCmd, liveText := cmdListen, "lmh-can listen "+res.Interface+" -protocol "+res.Protocol
	if res.Link == "rs485" {
		live = append(live, "-device", res.Class, "-baud", strconv.Itoa(res.Baud), "-parity", res.Parity,
			"-stop", strconv.Itoa(res.StopBits), "-slave", strconv.Itoa(res.Slave))
		liveCmd = cmdPoll
		liveText = "lmh-can poll " + strings.Join(live, " ")
	}
	switch res.Action {
	case wizard.ActionLive:
		fmt.Println()
		return liveCmd(live)
	case wizard.ActionService:
		fmt.Println()
		return cmdRun([]string{"-config", res.ConfigPath})
	}
	fmt.Printf("\nStart it later with:\n  %s\n", liveText)
	if res.MQTT {
		fmt.Printf("  lmh-can run -config %s\n", res.ConfigPath)
	} else {
		fmt.Printf("  lmh-can run -config %s -stdout\n", res.ConfigPath)
	}
	if res.Link == "can" {
		fmt.Println("The interface settings do not survive a reboot: see examples/lmh-can-iface@.service.")
	}
	return 0
}
