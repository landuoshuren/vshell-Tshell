//go:build !linux

package agent

import (
	"os"
	"os/exec"
	"path/filepath"
)

func runPluginPlatform(name string, payload []byte, args []string) ([]byte, error) {
	dir, err := os.MkdirTemp("", "vshell-plugin-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, filepath.Base(name))
	if err := os.WriteFile(path, payload, 0o700); err != nil {
		return nil, err
	}
	cmd := exec.Command(path, args...)
	cmd.Dir = dir
	return cmd.CombinedOutput()
}
