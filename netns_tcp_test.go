package services

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

// testdata/netns holds the field lines of a production pod's
// /proc/self/net/{netstat,snmp} (kernel of 2026-09-25) with the exported
// counters set to distinct values and the rest of TcpExt and Tcp zeroed.

func TestNetnsTCPCollectorExportsCounters(t *testing.T) {
	c, err := NewNetnsTCPCollector("testdata/netns")
	if err != nil {
		t.Fatal(err)
	}
	want := `
# HELP netns_tcp_aborts_on_memory_total Connections reset because TCP memory ran out (TcpExt TCPAbortOnMemory).
# TYPE netns_tcp_aborts_on_memory_total counter
netns_tcp_aborts_on_memory_total 2
# HELP netns_tcp_backlog_drops_total Segments dropped because the socket backlog was full (TcpExt TCPBacklogDrop).
# TYPE netns_tcp_backlog_drops_total counter
netns_tcp_backlog_drops_total 3
# HELP netns_tcp_memory_pressure_seconds_total Time the host spent in TCP memory pressure, counted in the namespace of the socket that ended it; summed over a node's namespaces it is the node's (TcpExt TCPMemoryPressuresChrono, ms).
# TYPE netns_tcp_memory_pressure_seconds_total counter
netns_tcp_memory_pressure_seconds_total 12.5
# HELP netns_tcp_memory_pressures_total Times the host entered TCP memory pressure (allocated > tcp_mem[1]), counted in the namespace of the socket that tipped it (TcpExt TCPMemoryPressures).
# TYPE netns_tcp_memory_pressures_total counter
netns_tcp_memory_pressures_total 9
# HELP netns_tcp_ofo_drops_total Out-of-order segments dropped for lack of receive memory (TcpExt TCPOFODrop).
# TYPE netns_tcp_ofo_drops_total counter
netns_tcp_ofo_drops_total 17
# HELP netns_tcp_rcvq_drops_total Incoming segments dropped because the socket's receive queue could not take them: receive buffer full, or TCP memory pressure (TcpExt TCPRcvQDrop).
# TYPE netns_tcp_rcvq_drops_total counter
netns_tcp_rcvq_drops_total 4242
# HELP netns_tcp_received_segments_total Segments received (Tcp InSegs).
# TYPE netns_tcp_received_segments_total counter
netns_tcp_received_segments_total 100000
# HELP netns_tcp_retransmitted_segments_total Segments retransmitted (Tcp RetransSegs).
# TYPE netns_tcp_retransmitted_segments_total counter
netns_tcp_retransmitted_segments_total 450
# HELP netns_tcp_sent_segments_total Segments sent, retransmissions included (Tcp OutSegs).
# TYPE netns_tcp_sent_segments_total counter
netns_tcp_sent_segments_total 90000
# HELP netns_tcp_timeouts_total Retransmission timeouts (RTO) fired; each one backs the connection off exponentially, up to 120 s (TcpExt TCPTimeouts).
# TYPE netns_tcp_timeouts_total counter
netns_tcp_timeouts_total 311
`
	if err := testutil.CollectAndCompare(c, strings.NewReader(want)); err != nil {
		t.Error(err)
	}
}

// A kernel without a field gets no series for it: a 0 would read as "no
// pressure" where the truth is "not measured".
func TestNetnsTCPFieldMissingIsOmitted(t *testing.T) {
	dir := t.TempDir()
	netstat, err := os.ReadFile("testdata/netns/netstat")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(netstat), "\n")
	for i := 0; i+1 < len(lines); i += 2 {
		if !strings.HasPrefix(lines[i], "TcpExt:") {
			continue
		}
		names, values := strings.Fields(lines[i]), strings.Fields(lines[i+1])
		for j := len(names) - 1; j > 0; j-- {
			if names[j] == "TCPMemoryPressuresChrono" {
				names = append(names[:j], names[j+1:]...)
				values = append(values[:j], values[j+1:]...)
			}
		}
		lines[i], lines[i+1] = strings.Join(names, " "), strings.Join(values, " ")
	}
	if err := os.WriteFile(filepath.Join(dir, "netstat"), []byte(strings.Join(lines, "\n")), 0644); err != nil {
		t.Fatal(err)
	}
	snmp, err := os.ReadFile("testdata/netns/snmp")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "snmp"), snmp, 0644); err != nil {
		t.Fatal(err)
	}
	c, err := NewNetnsTCPCollector(dir)
	if err != nil {
		t.Fatal(err)
	}
	if n := testutil.CollectAndCount(c, "netns_tcp_memory_pressure_seconds_total"); n != 0 {
		t.Errorf("%v series for a field the kernel does not have, want 0", n)
	}
	if n := testutil.CollectAndCount(c, "netns_tcp_memory_pressures_total"); n != 1 {
		t.Errorf("%v series for a field the kernel has, want 1", n)
	}
}

func TestNetnsTCPCollectorUnavailable(t *testing.T) {
	if _, err := NewNetnsTCPCollector(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Error("no error for a directory without the counters")
	}
}

func TestParseProcNetCountersRejectsMismatch(t *testing.T) {
	for name, in := range map[string]string{
		"short value line": "TcpExt: A B\nTcpExt: 1\n",
		"no value line":    "TcpExt: A B\n",
		"other protocol":   "TcpExt: A\nIpExt: 1\n",
		"not a number":     "TcpExt: A\nTcpExt: x\n",
	} {
		if _, err := parseProcNetCounters([]byte(in)); err == nil {
			t.Errorf("%v: no error", name)
		}
	}
}

// On Linux the process's own counters are there and parse.
func TestNetnsTCPCollectorOnThisHost(t *testing.T) {
	if _, err := os.Stat("/proc/self/net/netstat"); err != nil {
		t.Skip("no /proc/self/net here")
	}
	c, err := NewNetnsTCPCollector("/proc/self/net")
	if err != nil {
		t.Fatal(err)
	}
	if n := testutil.CollectAndCount(c, "netns_tcp_rcvq_drops_total"); n != 1 {
		t.Errorf("%v rcvq_drops series, want 1", n)
	}
}
