//go:build !unix

package executor

import (
	"os"
	"os/exec"
)

func configureProcessCancellation(cmd *exec.Cmd) {
	cmd.Cancel = func() error { return killProcessTree(cmd) }
}

// Non-Unix platforms can stop the direct process. A future platform-specific
// implementation can use job objects to also stop descendants.
func killProcessTree(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return os.ErrProcessDone
	}
	return cmd.Process.Kill()
}
