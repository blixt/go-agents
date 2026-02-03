package engine

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
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

	args := withInheritFDArgs(r.Args[1:], 3)
	cmd := exec.Command(r.Args[0], args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = append([]string{}, r.Env...)
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

func ListenerFromArgs(args []string) (net.Listener, error) {
	fd := -1
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if strings.HasPrefix(arg, "--inherit-fd=") {
			value := strings.TrimPrefix(arg, "--inherit-fd=")
			if value == "" {
				continue
			}
			parsed, err := strconv.Atoi(value)
			if err != nil {
				return nil, fmt.Errorf("invalid listener fd: %w", err)
			}
			fd = parsed
			break
		}
		if arg == "--inherit-fd" && i+1 < len(args) {
			parsed, err := strconv.Atoi(args[i+1])
			if err != nil {
				return nil, fmt.Errorf("invalid listener fd: %w", err)
			}
			fd = parsed
			break
		}
	}
	if fd < 0 {
		return nil, nil
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

func withInheritFDArgs(args []string, fd int) []string {
	cleaned := make([]string, 0, len(args)+1)
	skipNext := false
	for i := 0; i < len(args); i++ {
		if skipNext {
			skipNext = false
			continue
		}
		arg := args[i]
		if strings.HasPrefix(arg, "--inherit-fd=") {
			continue
		}
		if arg == "--inherit-fd" {
			skipNext = true
			continue
		}
		cleaned = append(cleaned, arg)
	}
	return append(cleaned, fmt.Sprintf("--inherit-fd=%d", fd))
}
