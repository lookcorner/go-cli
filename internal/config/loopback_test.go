package config

import (
	"net"
	"sync"
	"testing"
	"time"
)

// loopbackTCPAvailable probes once whether tests may open TCP connections to
// 127.0.0.1; some environments (endpoint filters) block loopback connects.
var loopbackTCPAvailable = sync.OnceValue(func() bool {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return false
	}
	defer listener.Close()
	done := make(chan struct{})
	go func() {
		conn, err := listener.Accept()
		if err == nil {
			_ = conn.Close()
		}
		close(done)
	}()
	conn, err := net.DialTimeout("tcp", listener.Addr().String(), 500*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	<-done
	return true
})

func requireLoopback(t *testing.T) {
	t.Helper()
	if !loopbackTCPAvailable() {
		t.Skip("loopback TCP connections are blocked in this environment")
	}
}
