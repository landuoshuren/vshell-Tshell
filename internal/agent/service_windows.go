//go:build windows

package agent

import (
	"os"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

func serviceInstall(name, description string) ([]byte, error) {
	manager, err := mgr.Connect()
	if err != nil {
		return nil, err
	}
	defer manager.Disconnect()
	executable, err := os.Executable()
	if err != nil {
		return nil, err
	}
	service, err := manager.CreateService(name, executable, mgr.Config{
		DisplayName: description,
		StartType:   windows.SERVICE_AUTO_START,
	})
	if err != nil {
		return nil, err
	}
	_ = service.Close()
	return []byte(""), nil
}

func serviceRemove(name string) ([]byte, error) {
	manager, err := mgr.Connect()
	if err != nil {
		return nil, err
	}
	defer manager.Disconnect()
	service, err := manager.OpenService(name)
	if err != nil {
		return nil, err
	}
	defer service.Close()
	_, _ = service.Control(svc.Stop)
	if err := service.Delete(); err != nil {
		return nil, err
	}
	return []byte(""), nil
}
