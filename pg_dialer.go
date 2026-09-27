package services

import (
	"context"
	"net"
	"syscall"
	"time"
)

// What go-pg v10 uses when Options.Dialer is nil (Options.init): a custom
// Dialer replaces that one entirely, DialTimeout included.
const (
	pgDialTimeout        = 5 * time.Second
	goPGDefaultKeepAlive = 5 * time.Minute
)

// newPGDialer returns the Options.Dialer for go-pg. It bounds how long a
// pooled connection to a server that vanished without a reset (unclean
// failover, address moved, network partition) can hold a query.
//
// go-pg sets no deadline of its own (ReadTimeout/WriteTimeout are 0) and its
// default dialer keeps TCP keepalive at 5 minutes. A query written into such
// a connection is never acknowledged, and Linux retransmits it for
// net.ipv4.tcp_retries2 (15 by default, ~15 minutes) before the read fails.
// Until then the connection holds its pool slot; once every slot is held
// that way, all other queries wait for PoolTimeout and fail.
//
//   - userTimeout sets TCP_USER_TIMEOUT: the kernel drops the connection
//     once sent data has stayed unacknowledged that long.
//   - keepAlive is the keepalive idle time and probe interval. It catches
//     connections that die while idle in the pool, which TCP_USER_TIMEOUT
//     alone does not: with no data in flight there is nothing to time out.
//     With TCP_USER_TIMEOUT set, Linux drops an idle connection at the first
//     probe tick at which nothing has been received for userTimeout, i.e.
//     after max(2*keepAlive, userTimeout rounded up to keepAlive) of silence;
//     keepAlive = userTimeout/2 makes that the same bound as a busy one.
//
// Both act only on the peer's kernel going silent. A live server
// acknowledges data and keepalive probes while its backend is busy, so a
// long query or an idle LISTEN is not affected. A ReadTimeout would end the
// same hang but also every legitimate query that runs longer than it
// (migrations, index builds, batch jobs), because it bounds the wait for the
// answer rather than the delivery of the request.
//
// userTimeout <= 0 leaves the kernel default. keepAlive <= 0 keeps go-pg's
// 5-minute keepalive, so both at 0 dial exactly as go-pg's default dialer.
// TCP_USER_TIMEOUT is Linux-only; elsewhere it is not set.
func newPGDialer(dialTimeout, keepAlive, userTimeout time.Duration) func(ctx context.Context, network, addr string) (net.Conn, error) {
	d := &net.Dialer{Timeout: dialTimeout}
	if keepAlive > 0 {
		d.KeepAliveConfig = net.KeepAliveConfig{
			Enable:   true,
			Idle:     keepAlive,
			Interval: keepAlive,
		}
	} else {
		d.KeepAlive = goPGDefaultKeepAlive
	}
	if userTimeout > 0 {
		d.Control = func(_, _ string, rc syscall.RawConn) error {
			return setTCPUserTimeout(rc, userTimeout)
		}
	}
	return d.DialContext
}
