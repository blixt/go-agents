package engine

import (
	"net"
	"strconv"
	"testing"
)

func TestListenerFromArgs(t *testing.T) {
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

	got, err := ListenerFromArgs([]string{"agentd", "--inherit-fd", strconv.Itoa(int(file.Fd()))})
	if err != nil {
		t.Fatalf("listener from args: %v", err)
	}
	if got == nil {
		t.Fatalf("expected listener")
	}
	_ = got.Close()
}
