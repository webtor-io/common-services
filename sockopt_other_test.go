//go:build !linux && !darwin

package services

import (
	"net"
	"testing"
)

type sockOpts struct {
	keepAlive        bool
	idle, interval   int
	userTimeoutMS    int
	userTimeoutKnown bool
}

func readSockOpts(t *testing.T, _ net.Conn) sockOpts {
	t.Skip("socket options are read back on linux and darwin only")
	return sockOpts{}
}
