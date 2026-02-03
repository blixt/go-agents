package engine

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
)

type Restarter struct {
	Listener net.Listener
	Args     []string
	Env      []string
}

func (r *Restarter) Restart() error {
	if r.Listener == nil {
		return fmt.Errorf("listener not set")
	}
	if len(r.Args) == 0 {
		return fmt.Errorf("args not set")
	}
	file, err := listenerFile(r.Listener)
	if err != nil {
		return err
	}

	cmd := exec.Command(r.Args[0], r.Args[1:]...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = append(append([]string{}, r.Env...), "GO_AGENTS_INHERIT_FD=1", "GO_AGENTS_FD=3")
	cmd.ExtraFiles = []*os.File{file}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start new process: %w", err)
	}
	return nil
}

func listenerFile(listener net.Listener) (*os.File, error) {
	switch ln := listener.(type) {
	case *net.TCPListener:
		file, err := ln.File()
		if err != nil {
			return nil, fmt.Errorf("listener file: %w", err)
		}
		return file, nil
	default:
		return nil, fmt.Errorf("unsupported listener type %T", listener)
	}
}

func ListenerFromEnv() (net.Listener, error) {
	if os.Getenv("GO_AGENTS_INHERIT_FD") != "1" {
		return nil, nil
	}
	fdStr := os.Getenv("GO_AGENTS_FD")
	if fdStr == "" {
		fdStr = "3"
	}
	fd, err := strconv.Atoi(fdStr)
	if err != nil {
		return nil, fmt.Errorf("invalid listener fd: %w", err)
	}
	file := os.NewFile(uintptr(fd), "listener")
	if file == nil {
		return nil, fmt.Errorf("failed to create listener file")
	}
	ln, err := net.FileListener(file)
	if err != nil {
		return nil, fmt.Errorf("file listener: %w", err)
	}
	return ln, nil
}
