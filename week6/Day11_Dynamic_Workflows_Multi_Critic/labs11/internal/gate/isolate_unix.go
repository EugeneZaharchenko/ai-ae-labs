//go:build unix

package gate

import (
	"os/exec"
	"syscall"
)

// isolate puts the child in its own process group and kills the whole group on
// cancellation, so a `go test` binary spawned by `go test` dies with it.
func isolate(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
}
