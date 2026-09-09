//go:build !windows

package main

import (
	"errors"
	"net"
)

func dialNamedPipe(string) (net.Conn, error) {
	return nil, errors.New("named pipes supported on windows only")
}
