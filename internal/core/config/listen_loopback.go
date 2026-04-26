package config

import (
	"net"
	"strings"
)

func IsExplicitLoopbackListenAddress(raw string) bool {
	addr := strings.TrimSpace(raw)
	if addr == "" {
		return false
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	host = strings.TrimSpace(host)
	if host == "" {
		return false
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return true
	}
	return strings.EqualFold(host, "localhost")
}
