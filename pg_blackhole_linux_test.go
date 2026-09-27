//go:build linux && pgblackhole

// Postgres behind an address that stops answering without a reset: an
// unclean failover moves the server, and pooled connections to the old
// address see silence rather than an error. Run with testdata/pgblackhole/run.sh
// (a container with NET_ADMIN next to a Postgres reachable on two addresses).
//
// The bounds assert the default configuration (TCP_USER_TIMEOUT 30s,
// keepalive 15s). Without the dialer each case is still blocked at
// pgbhWait, where go-pg alone waits out tcp_retries2 (~15 min).

package services

import (
	"bufio"
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-pg/pg/v10"
)

const (
	pgbhHost = "pgbh"
	pgbhPort = 5432
	pgbhWait = 90 * time.Second
)

func pgbhIP(t *testing.T, env string) string {
	t.Helper()
	ip := os.Getenv(env)
	if ip == "" {
		t.Skipf("%s not set; run testdata/pgblackhole/run.sh", env)
	}
	return ip
}

func iptables(t *testing.T, args ...string) {
	t.Helper()
	if out, err := exec.Command("iptables", args...).CombinedOutput(); err != nil {
		t.Fatalf("iptables %v: %v: %s", args, err, out)
	}
}

// blackhole drops traffic to and from ip's Postgres port both ways, as a
// host that vanished would, and restores it at cleanup.
func blackhole(t *testing.T, ip string) (restore func()) {
	t.Helper()
	port := strconv.Itoa(pgbhPort)
	out := []string{"OUTPUT", "-d", ip, "-p", "tcp", "--dport", port, "-j", "DROP"}
	in := []string{"INPUT", "-s", ip, "-p", "tcp", "--sport", port, "-j", "DROP"}
	iptables(t, append([]string{"-I"}, out...)...)
	iptables(t, append([]string{"-I"}, in...)...)
	var once sync.Once
	restore = func() {
		once.Do(func() {
			iptables(t, append([]string{"-D"}, out...)...)
			iptables(t, append([]string{"-D"}, in...)...)
		})
	}
	t.Cleanup(restore)
	return restore
}

// pointHost makes pgbhHost resolve to ip, as DNS does after a failover.
func pointHost(t *testing.T, ip string) {
	t.Helper()
	orig, err := os.ReadFile("/etc/hosts")
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for _, l := range strings.Split(strings.TrimRight(string(orig), "\n"), "\n") {
		if f := strings.Fields(l); len(f) >= 2 && f[1] == pgbhHost {
			continue
		}
		b.WriteString(l + "\n")
	}
	fmt.Fprintf(&b, "%s\t%s\n", ip, pgbhHost)
	if err := os.WriteFile("/etc/hosts", []byte(b.String()), 0644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.WriteFile("/etc/hosts", orig, 0644) })
}

// established counts this namespace's ESTABLISHED TCP sockets to ip:port
// (/proc/net/tcp, IPv4). A socket the kernel gave up on leaves the table
// even while the process still holds its descriptor.
func established(t *testing.T, ip string, port int) int {
	t.Helper()
	f, err := os.Open("/proc/net/tcp")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	ip4 := net.ParseIP(ip).To4()
	want := fmt.Sprintf("%08X:%04X", binary.LittleEndian.Uint32(ip4), port)
	n := 0
	sc := bufio.NewScanner(f)
	sc.Scan() // header
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) > 3 && strings.EqualFold(fields[2], want) && fields[3] == "01" {
			n++
		}
	}
	return n
}

func newBlackholePG(t *testing.T, args ...string) *pg.DB {
	t.Helper()
	s, err := newPGFromCLI(args...)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("tcpUserTimeout=%v keepAlive=%v maxRetries=%d poolSize=%d ssl=%v",
		s.tcpUserTimeout, s.keepAlive, s.maxRetries, s.poolSize, s.ssl)
	db := s.Get()
	if db == nil {
		t.Fatal("PG_HOST not set")
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// warm opens n pooled connections by running n queries at once.
func warm(t *testing.T, db *pg.DB, n int) {
	t.Helper()
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := db.Exec("SELECT pg_sleep(0.3)")
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
}

// timedQuery runs SELECT 1 and waits at most pgbhWait for it.
func timedQuery(db *pg.DB) (elapsed time.Duration, err error, done bool) {
	start := time.Now()
	ch := make(chan error, 1)
	go func() {
		_, err := db.ExecContext(context.Background(), "SELECT 1")
		ch <- err
	}()
	select {
	case err := <-ch:
		return time.Since(start), err, true
	case <-time.After(pgbhWait):
		return time.Since(start), nil, false
	}
}

// recovered polls SELECT 1 until it succeeds, for at most limit.
func recovered(t *testing.T, db *pg.DB, limit time.Duration) time.Duration {
	t.Helper()
	start := time.Now()
	var last error
	for time.Since(start) < limit {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_, last = db.ExecContext(ctx, "SELECT 1")
		cancel()
		if last == nil {
			return time.Since(start)
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("no successful query %v after the address came back; last error: %v", limit, last)
	return 0
}

// A query sent into a dead connection fails once TCP_USER_TIMEOUT expires
// (plus go-pg's cancel request, which dials the same dead address for up to
// the 5 s dial timeout). Retries off, to time one connection.
func TestPGBlackholeInFlightQuery(t *testing.T) {
	ipA := pgbhIP(t, "PGBH_IP_A")
	db := newBlackholePG(t, "--postgres-max-retries", "0", "--postgres-min-idle-conns", "0")
	warm(t, db, 1)
	restore := blackhole(t, ipA)

	elapsed, err, done := timedQuery(db)
	if !done {
		t.Fatalf("query still blocked after %v", elapsed.Round(time.Second))
	}
	t.Logf("query failed after %v: %v", elapsed.Round(100*time.Millisecond), err)
	if err == nil {
		t.Fatal("query on a blackholed connection succeeded")
	}
	if elapsed < 25*time.Second || elapsed > 40*time.Second {
		t.Errorf("failed after %v, want 30-35 s (TCP_USER_TIMEOUT + cancel dial)", elapsed)
	}

	restore()
	t.Logf("first successful query %v after the address came back", recovered(t, db, 15*time.Second).Round(100*time.Millisecond))
}

// Same outage with go-pg's retries on (the service default): each retry
// redials the dead address for up to the dial timeout.
func TestPGBlackholeInFlightQueryWithRetries(t *testing.T) {
	ipA := pgbhIP(t, "PGBH_IP_A")
	db := newBlackholePG(t)
	warm(t, db, 1)
	restore := blackhole(t, ipA)

	elapsed, err, done := timedQuery(db)
	if !done {
		t.Fatalf("query still blocked after %v", elapsed.Round(time.Second))
	}
	t.Logf("query failed after %v: %v", elapsed.Round(100*time.Millisecond), err)
	if err == nil {
		t.Fatal("query on a blackholed connection succeeded")
	}
	if elapsed > 60*time.Second {
		t.Errorf("failed after %v, want under 60 s", elapsed)
	}

	restore()
	t.Logf("first successful query %v after the address came back", recovered(t, db, 15*time.Second).Round(100*time.Millisecond))
}

// Idle pooled connections to the dead address are dropped by the kernel via
// keepalive, without a query touching them.
func TestPGBlackholeIdleConnsDropped(t *testing.T) {
	ipA := pgbhIP(t, "PGBH_IP_A")
	db := newBlackholePG(t)
	warm(t, db, 5)
	n0 := established(t, ipA, pgbhPort)
	if n0 < 5 {
		t.Fatalf("%d established connections after warm-up, want >= 5", n0)
	}
	blackhole(t, ipA)
	start := time.Now()
	for {
		n := established(t, ipA, pgbhPort)
		if n == 0 {
			break
		}
		if time.Since(start) > pgbhWait {
			t.Fatalf("%d of %d connections still established after %v", n, n0, pgbhWait)
		}
		time.Sleep(250 * time.Millisecond)
	}
	elapsed := time.Since(start)
	t.Logf("all %d idle connections dropped after %v", n0, elapsed.Round(100*time.Millisecond))
	if elapsed > 35*time.Second {
		t.Errorf("idle connections dropped after %v, want within ~30 s", elapsed)
	}
}

// The incident: the server comes back on another address, DNS follows, and
// the pool still holds connections to the old one. The first query can still
// fail: once its own connection times out, go-pg retries on the pool's other
// dead connections, which the kernel has dropped by then, so each fails at
// once and uses up a retry. The ones left over go the same way on the next
// query, whose retry then dials the new address.
func TestPGBlackholeFailover(t *testing.T) {
	ipA := pgbhIP(t, "PGBH_IP_A")
	ipB := pgbhIP(t, "PGBH_IP_B")
	pointHost(t, ipA)
	db := newBlackholePG(t)
	warm(t, db, 5)
	blackhole(t, ipA)
	pointHost(t, ipB)

	elapsed, err, done := timedQuery(db)
	if !done {
		t.Fatalf("first query after failover still blocked after %v", elapsed.Round(time.Second))
	}
	t.Logf("first query after failover returned after %v, err=%v", elapsed.Round(100*time.Millisecond), err)
	if elapsed > 45*time.Second {
		t.Errorf("first query took %v, want within ~35 s", elapsed)
	}

	var fails int
	start := time.Now()
	for i := 0; i < 20; i++ {
		e, err, done := timedQuery(db)
		if !done {
			t.Fatalf("query %d after failover blocked for %v", i, e.Round(time.Second))
		}
		if err != nil {
			fails++
			t.Logf("query %d: %v after %v", i, err, e.Round(100*time.Millisecond))
		}
	}
	t.Logf("next 20 queries: %d failed, took %v in total", fails, time.Since(start).Round(100*time.Millisecond))
	if fails > 1 {
		t.Errorf("%d of the next 20 queries failed, want at most 1", fails)
	}
	if n := established(t, ipA, pgbhPort); n != 0 {
		t.Errorf("%d connections to the old address still established", n)
	}
}

// A live server that takes longer than TCP_USER_TIMEOUT to answer is not cut
// off: its kernel acknowledges the query and the keepalive probes. This is
// what a ReadTimeout of the same length would break.
func TestPGLongQueryOnLiveServer(t *testing.T) {
	pgbhIP(t, "PGBH_IP_A")
	db := newBlackholePG(t, "--postgres-max-retries", "0")
	start := time.Now()
	if _, err := db.Exec("SELECT pg_sleep(45)"); err != nil {
		t.Fatalf("45 s query failed after %v: %v", time.Since(start).Round(100*time.Millisecond), err)
	}
	t.Logf("45 s query succeeded after %v", time.Since(start).Round(100*time.Millisecond))
}
