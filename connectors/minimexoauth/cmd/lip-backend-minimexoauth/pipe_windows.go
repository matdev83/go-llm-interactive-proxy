//go:build windows

package main

import (
	"net"
	"time"

	"github.com/Microsoft/go-winio"
)

func dialNamedPipe(path string) (net.Conn, error) {
	timeout := 5 * time.Second
	return winio.DialPipe(path, &timeout)
}
