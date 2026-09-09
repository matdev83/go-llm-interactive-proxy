//go:build !windows

package main

import (
	"errors"
	"net"
)

func dialNamedPipe(_ string) (net.Conn, error) {
	return nil, errors.New("named pipes not supported on this platform")
}
