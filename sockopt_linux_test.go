//go:build linux

package services

import (
	"net"
	"testing"

	"golang.org/x/sys/unix"
)

type sockOpts struct {
	keepAlive        bool
	idle, interval   int // seconds
	userTimeoutMS    int
	userTimeoutKnown bool
}

func readSockOpts(t *testing.T, c net.Conn) sockOpts {
	t.Helper()
	rc, err := c.(*net.TCPConn).SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	var o sockOpts
	var errs []error
	get := func(fd int, level, opt int) int {
		v, err := unix.GetsockoptInt(fd, level, opt)
		if err != nil {
			errs = append(errs, err)
		}
		return v
	}
	if err := rc.Control(func(fd uintptr) {
		f := int(fd)
		o.keepAlive = get(f, unix.SOL_SOCKET, unix.SO_KEEPALIVE) != 0
		o.idle = get(f, unix.IPPROTO_TCP, unix.TCP_KEEPIDLE)
		o.interval = get(f, unix.IPPROTO_TCP, unix.TCP_KEEPINTVL)
		o.userTimeoutMS = get(f, unix.IPPROTO_TCP, unix.TCP_USER_TIMEOUT)
		o.userTimeoutKnown = true
	}); err != nil {
		t.Fatal(err)
	}
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	return o
}
