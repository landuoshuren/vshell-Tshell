//go:build !windows

package agent

import "errors"

func screenControl(map[string]any) error {
	return errors.New("screen control is currently implemented for windows")
}
