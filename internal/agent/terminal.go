package agent

import (
	"bytes"
	"errors"
	"os"
	"runtime"
	"sync"
	"time"

	ptylib "github.com/aymanbagabas/go-pty"
)

const terminalBufferLimit = 8 << 20

type terminalSession struct {
	pty       ptylib.Pty
	cmd       *ptylib.Cmd
	bufferMu  sync.Mutex
	writeMu   sync.Mutex
	buffer    bytes.Buffer
	closed    chan struct{}
	closeOnce sync.Once
}

func (a *Agent) openTerminal() (string, string, error) {
	terminal, err := ptylib.New()
	if err != nil {
		return "", "", err
	}
	_ = terminal.Resize(128, 40)
	shell, arguments := terminalShell()
	command := terminal.Command(shell, arguments...)
	command.Env = os.Environ()
	if runtime.GOOS != "windows" && os.Getenv("TERM") == "" {
		command.Env = append(command.Env, "TERM=xterm-256color")
	}
	if err = command.Start(); err != nil {
		_ = terminal.Close()
		return "", "", err
	}
	session := &terminalSession{pty: terminal, cmd: command, closed: make(chan struct{})}
	id := randomKey() + randomKey()
	a.terminalMu.Lock()
	a.terminals[id] = session
	a.terminalMu.Unlock()
	go session.capture()
	go func() {
		_ = command.Wait()
		session.close()
		a.terminalMu.Lock()
		if a.terminals[id] == session {
			delete(a.terminals, id)
		}
		a.terminalMu.Unlock()
	}()
	time.Sleep(80 * time.Millisecond)
	return id, session.drain(), nil
}

func terminalShell() (string, []string) {
	if runtime.GOOS == "windows" {
		shell := os.Getenv("COMSPEC")
		if shell == "" {
			shell = "cmd.exe"
		}
		return shell, []string{"/Q"}
	}
	shell := os.Getenv("SHELL")
	if shell == "" {
		if _, err := os.Stat("/bin/bash"); err == nil {
			shell = "/bin/bash"
		} else {
			shell = "/bin/sh"
		}
	}
	return shell, nil
}

func (s *terminalSession) capture() {
	buffer := make([]byte, 32<<10)
	for {
		n, err := s.pty.Read(buffer)
		if n > 0 {
			s.bufferMu.Lock()
			if s.buffer.Len()+n > terminalBufferLimit {
				trim := s.buffer.Len() + n - terminalBufferLimit
				if trim > 0 && trim <= s.buffer.Len() {
					_ = s.buffer.Next(trim)
				}
			}
			_, _ = s.buffer.Write(buffer[:n])
			s.bufferMu.Unlock()
		}
		if err != nil {
			return
		}
	}
}

func (s *terminalSession) drain() string {
	s.bufferMu.Lock()
	defer s.bufferMu.Unlock()
	output := s.buffer.String()
	s.buffer.Reset()
	return output
}

func (a *Agent) terminal(id string) (*terminalSession, error) {
	a.terminalMu.Lock()
	defer a.terminalMu.Unlock()
	session := a.terminals[id]
	if session == nil {
		return nil, errors.New("terminal is close")
	}
	return session, nil
}

func (a *Agent) writeTerminal(id, content string) (string, error) {
	session, err := a.terminal(id)
	if err != nil {
		return "", err
	}
	session.writeMu.Lock()
	_, err = session.pty.Write([]byte(content))
	session.writeMu.Unlock()
	if err != nil {
		return "", err
	}
	time.Sleep(35 * time.Millisecond)
	return session.drain(), nil
}

func (a *Agent) readTerminal(id string) (string, error) {
	session, err := a.terminal(id)
	if err != nil {
		return "", err
	}
	return session.drain(), nil
}

func (a *Agent) closeTerminal(id string) error {
	a.terminalMu.Lock()
	session := a.terminals[id]
	delete(a.terminals, id)
	a.terminalMu.Unlock()
	if session == nil {
		return nil
	}
	session.close()
	return nil
}

func (s *terminalSession) close() {
	s.closeOnce.Do(func() {
		close(s.closed)
		_ = s.pty.Close()
		if s.cmd != nil && s.cmd.Process != nil {
			_ = s.cmd.Process.Kill()
		}
	})
}
