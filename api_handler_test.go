package main

import (
	"io"
	"net"
	"net/http"
	"testing"
	"time"
)

func TestAPIServerClosesConnectionWithUnfinishedHeaders(t *testing.T) {
	old := apiReadHeaderTimeout
	apiReadHeaderTimeout = 100 * time.Millisecond
	t.Cleanup(func() { apiReadHeaderTimeout = old })

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := newAPIServer(ln.Addr().String(), http.NewServeMux())
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })

	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if _, err := conn.Write([]byte("GET /api/v1/placements HTTP/1.1\r\nHost: x\r\n")); err != nil {
		t.Fatal(err)
	}
	if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadAll(conn); err != nil {
		t.Fatalf("server kept the connection open: %v", err)
	}
}
