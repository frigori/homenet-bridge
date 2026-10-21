// Package awake keeps the machine from sleeping while a sharing session runs.
package awake

import (
	"os"
	"os/exec"
	"strconv"
)

// Hold prevents idle sleep until release is called or this process exits. It uses
// caffeinate on macOS and systemd-inhibit on Linux; elsewhere it does nothing and ok is false.
func Hold(reason string) (release func(), ok bool) {
	pid := strconv.Itoa(os.Getpid())
	var cmd *exec.Cmd
	if p, err := exec.LookPath("caffeinate"); err == nil {
		// -i: no idle sleep; -w: stop by itself if we die.
		cmd = exec.Command(p, "-i", "-w", pid)
	} else if p, err := exec.LookPath("systemd-inhibit"); err == nil {
		cmd = exec.Command(p, "--what=idle:sleep", "--who=homenet-bridge", "--why="+reason, "--mode=block",
			"tail", "--pid="+pid, "-f", "/dev/null")
	} else {
		return func() {}, false
	}
	if err := cmd.Start(); err != nil {
		return func() {}, false
	}
	return func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}, true
}
