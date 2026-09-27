package services

import (
	"context"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-pg/pg/v10"
	"github.com/urfave/cli"
)

func unsetenv(t *testing.T, keys ...string) {
	t.Helper()
	for _, k := range keys {
		t.Setenv(k, "")
		if err := os.Unsetenv(k); err != nil {
			t.Fatal(err)
		}
	}
}

// newPGFromCLI builds PG the way services do: RegisterPGFlags + NewPG.
func newPGFromCLI(args ...string) (*PG, error) {
	var s *PG
	app := cli.NewApp()
	app.Flags = RegisterPGFlags(nil)
	app.Action = func(c *cli.Context) error {
		s = NewPG(c)
		return nil
	}
	err := app.Run(append([]string{"test"}, args...))
	return s, err
}

func TestPGDeadConnFlags(t *testing.T) {
	unsetenv(t, "PG_TCP_USER_TIMEOUT", "PG_KEEPALIVE")
	s, err := newPGFromCLI()
	if err != nil {
		t.Fatal(err)
	}
	if s.tcpUserTimeout != 30*time.Second || s.keepAlive != 15*time.Second {
		t.Errorf("defaults: tcpUserTimeout=%v keepAlive=%v, want 30s 15s", s.tcpUserTimeout, s.keepAlive)
	}

	t.Setenv("PG_TCP_USER_TIMEOUT", "0")
	t.Setenv("PG_KEEPALIVE", "45s")
	s, err = newPGFromCLI()
	if err != nil {
		t.Fatal(err)
	}
	if s.tcpUserTimeout != 0 || s.keepAlive != 45*time.Second {
		t.Errorf("env: tcpUserTimeout=%v keepAlive=%v, want 0 45s", s.tcpUserTimeout, s.keepAlive)
	}
}

// A value without a unit must stop the service at start. Read as 0 it would
// silently switch the protection off.
func TestPGDeadConnFlagRejectsBadDuration(t *testing.T) {
	unsetenv(t, "PG_KEEPALIVE")
	t.Setenv("PG_TCP_USER_TIMEOUT", "30")
	if _, err := newPGFromCLI(); err == nil || !strings.Contains(err.Error(), pgTCPUserTimeoutFlag) {
		t.Fatalf("err = %v, want a parse error naming the flag", err)
	}
}

// dialSockOpts dials a local listener with dial and returns the socket
// options the kernel reports for the client side.
func dialSockOpts(t *testing.T, dial func(ctx context.Context, network, addr string) (net.Conn, error)) sockOpts {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		c, err := ln.Accept()
		if err == nil {
			defer c.Close()
			_, _ = c.Read(make([]byte, 1))
		}
	}()
	conn, err := dial(context.Background(), "tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	return readSockOpts(t, conn)
}

// The dialer PG hands to go-pg sets keepalive and TCP_USER_TIMEOUT from the
// flags. Read back from the kernel, not from the Dialer struct: a Dialer
// field that never reaches the socket is the failure this guards against.
func TestPGDialerSetsSocketOptions(t *testing.T) {
	unsetenv(t, "PG_TCP_USER_TIMEOUT", "PG_KEEPALIVE")
	s, err := newPGFromCLI("--postgres-host", "127.0.0.1", "--postgres-min-idle-conns", "0")
	if err != nil {
		t.Fatal(err)
	}
	db := s.get()
	defer db.Close()

	got := dialSockOpts(t, db.Options().Dialer)
	if !got.keepAlive || got.idle != 15 || got.interval != 15 {
		t.Errorf("keepalive=%v idle=%ds interval=%ds, want on 15s 15s", got.keepAlive, got.idle, got.interval)
	}
	if got.userTimeoutKnown && got.userTimeoutMS != 30000 {
		t.Errorf("TCP_USER_TIMEOUT=%dms, want 30000", got.userTimeoutMS)
	}
	if !got.userTimeoutKnown {
		t.Log("TCP_USER_TIMEOUT not checked on this OS")
	}
}

// With both flags at 0 the socket is configured exactly as by go-pg's own
// default dialer: the switch-off is a real rollback.
func TestPGDialerDisabledMatchesGoPGDefault(t *testing.T) {
	t.Setenv("PG_TCP_USER_TIMEOUT", "0")
	t.Setenv("PG_KEEPALIVE", "0")
	s, err := newPGFromCLI("--postgres-host", "127.0.0.1", "--postgres-min-idle-conns", "0")
	if err != nil {
		t.Fatal(err)
	}
	db := s.get()
	defer db.Close()
	ours := dialSockOpts(t, db.Options().Dialer)

	def := pg.Connect(&pg.Options{Addr: "127.0.0.1:1"})
	defer def.Close()
	theirs := dialSockOpts(t, def.Options().Dialer)

	if ours != theirs {
		t.Errorf("disabled dialer %+v, go-pg default %+v", ours, theirs)
	}
	if theirs.idle != 300 {
		t.Errorf("go-pg default keepalive idle=%ds, want 300 (did go-pg change its default?)", theirs.idle)
	}
}
