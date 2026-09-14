//go:build linux

package agent

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"golang.org/x/sys/unix"
)

func runPluginPlatform(name string, payload []byte, args []string) ([]byte, error) {
	fd, err := unix.MemfdCreate("", unix.MFD_CLOEXEC)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), "")
	defer f.Close()
	if _, err = f.Write(payload); err != nil {
		return nil, err
	}
	if err = unix.Fchmod(fd, 0o700); err != nil {
		return nil, err
	}
	fdPath := fmt.Sprintf("/proc/%d/fd/%d", os.Getpid(), fd)
	var cmd *exec.Cmd
	if strings.HasSuffix(strings.ToLower(name), ".so") {
		cmd = exec.Command("/bin/ls", args...)
		cmd.Env = append(os.Environ(), "LD_PRELOAD="+fdPath)
	} else {
		cmd = exec.Command(fdPath, args...)
	}
	out, runErr := cmd.CombinedOutput()
	if _, exited := runErr.(*exec.ExitError); exited {
		return out, nil
	}
	return out, runErr
}
