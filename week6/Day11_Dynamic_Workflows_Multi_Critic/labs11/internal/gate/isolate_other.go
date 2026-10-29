//go:build !unix

package gate

import "os/exec"

// isolate is a no-op off unix: the default CommandContext kill still applies.
func isolate(*exec.Cmd) {}
