//go:build windows

package hosttool

import "os/exec"

func own(*exec.Cmd) {}

func terminate(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}

func kill(cmd *exec.Cmd) { terminate(cmd) }
