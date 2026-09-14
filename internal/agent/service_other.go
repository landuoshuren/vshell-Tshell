//go:build !windows

package agent

import (
	"fmt"
	"os"
	"os/exec"
)

func serviceInstall(name, description string) ([]byte, error) {
	executable, err := os.Executable()
	if err != nil {
		return nil, err
	}
	unit := fmt.Sprintf(`[Unit]
Description=%s
ConditionFileIsExecutable=%s


[Service]
StartLimitInterval=5
StartLimitBurst=10
ExecStart=%s



Restart=always

RestartSec=120
EnvironmentFile=-/etc/sysconfig/%s

[Install]
WantedBy=multi-user.target
`, description, executable, executable, name)
	path := "/etc/systemd/system/" + name + ".service"
	if err := os.WriteFile(path, []byte(unit), 0o644); err != nil {
		return nil, err
	}
	if out, err := exec.Command("systemctl", "daemon-reload").CombinedOutput(); err != nil {
		return out, err
	}
	if out, err := exec.Command("systemctl", "enable", name).CombinedOutput(); err != nil {
		return out, err
	}
	return []byte("Service Install Success"), nil
}

func serviceRemove(name string) ([]byte, error) {
	_, _ = exec.Command("systemctl", "disable", "--now", name).CombinedOutput()
	if err := os.Remove("/etc/systemd/system/" + name + ".service"); err != nil {
		return nil, err
	}
	_, _ = exec.Command("systemctl", "daemon-reload").CombinedOutput()
	return []byte("Service Remove Success"), nil
}
