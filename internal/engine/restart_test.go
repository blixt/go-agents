package engine

import (
	"net"
	"os"
	"strconv"
	"testing"
)

func TestListenerFromEnv(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	tcpLn, ok := ln.(*net.TCPListener)
	if !ok {
		t.Fatalf("expected TCP listener")
	}
	file, err := tcpLn.File()
	if err != nil {
		t.Fatalf("listener file: %v", err)
	}
	defer file.Close()

	prevInherit := os.Getenv("GO_AGENTS_INHERIT_FD")
	prevFD := os.Getenv("GO_AGENTS_FD")
	defer func() {
		_ = os.Setenv("GO_AGENTS_INHERIT_FD", prevInherit)
		_ = os.Setenv("GO_AGENTS_FD", prevFD)
	}()

	_ = os.Setenv("GO_AGENTS_INHERIT_FD", "1")
	_ = os.Setenv("GO_AGENTS_FD", strconv.Itoa(int(file.Fd())))

	got, err := ListenerFromEnv()
	if err != nil {
		t.Fatalf("listener from env: %v", err)
	}
	if got == nil {
		t.Fatalf("expected listener")
	}
	_ = got.Close()
}
