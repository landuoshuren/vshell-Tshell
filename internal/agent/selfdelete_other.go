//go:build !windows

package agent

import "os"

func deleteRunningExecutable() error {
	path, err := os.Executable()
	if err != nil {
		return err
	}
	return os.Remove(path)
}
