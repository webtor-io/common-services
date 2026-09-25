package services

import (
	"bufio"
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/pkg/errors"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/sirupsen/logrus"
)

// NetnsTCPCollector exports the TCP counters of the network namespace the
// process runs in, read from /proc/self/net/{netstat,snmp} on every scrape.
// In a pod that is the pod's own namespace. node-exporter only reads the
// host's, and the kernel keeps these counters per namespace, so without this
// the drops and memory-pressure episodes of torrent-http-proxy and the
// seeders are invisible to Prometheus. On 2026-09-25 they were the cause of
// playback stalls: 16 MiB receive queues pushed the nodes into TCP memory
// pressure 59-83% of the time and the proxies dropped 50-180 segments/s.
//
// Processes sharing a namespace (host network, a single-container
// deployment) report the same numbers.
type NetnsTCPCollector struct {
	dir string
}

type netnsTCPCounter struct {
	file, proto, field string
	desc               *prometheus.Desc
	scale              float64
}

var netnsTCPCounters = []netnsTCPCounter{
	{"netstat", "TcpExt", "TCPRcvQDrop", netnsTCPDesc("rcvq_drops_total",
		"Incoming segments dropped because the socket's receive queue could not take them: receive buffer full, or TCP memory pressure (TcpExt TCPRcvQDrop)."), 1},
	{"netstat", "TcpExt", "TCPOFODrop", netnsTCPDesc("ofo_drops_total",
		"Out-of-order segments dropped for lack of receive memory (TcpExt TCPOFODrop)."), 1},
	{"netstat", "TcpExt", "TCPBacklogDrop", netnsTCPDesc("backlog_drops_total",
		"Segments dropped because the socket backlog was full (TcpExt TCPBacklogDrop)."), 1},
	{"netstat", "TcpExt", "TCPMemoryPressures", netnsTCPDesc("memory_pressures_total",
		"Times the host entered TCP memory pressure (allocated > tcp_mem[1]), counted in the namespace of the socket that tipped it (TcpExt TCPMemoryPressures)."), 1},
	{"netstat", "TcpExt", "TCPMemoryPressuresChrono", netnsTCPDesc("memory_pressure_seconds_total",
		"Time the host spent in TCP memory pressure, counted in the namespace of the socket that ended it; summed over a node's namespaces it is the node's (TcpExt TCPMemoryPressuresChrono, ms)."), 0.001},
	{"netstat", "TcpExt", "TCPAbortOnMemory", netnsTCPDesc("aborts_on_memory_total",
		"Connections reset because TCP memory ran out (TcpExt TCPAbortOnMemory)."), 1},
	{"netstat", "TcpExt", "TCPTimeouts", netnsTCPDesc("timeouts_total",
		"Retransmission timeouts (RTO) fired; each one backs the connection off exponentially, up to 120 s (TcpExt TCPTimeouts)."), 1},
	{"snmp", "Tcp", "RetransSegs", netnsTCPDesc("retransmitted_segments_total",
		"Segments retransmitted (Tcp RetransSegs)."), 1},
	{"snmp", "Tcp", "OutSegs", netnsTCPDesc("sent_segments_total",
		"Segments sent, retransmissions included (Tcp OutSegs)."), 1},
	{"snmp", "Tcp", "InSegs", netnsTCPDesc("received_segments_total",
		"Segments received (Tcp InSegs)."), 1},
}

func netnsTCPDesc(name, help string) *prometheus.Desc {
	return prometheus.NewDesc("netns_tcp_"+name, help, nil, nil)
}

// NewNetnsTCPCollector reads the counters under dir (/proc/self/net in
// production). It fails when they cannot be read, so a platform without them
// is reported once at start-up instead of on every scrape.
func NewNetnsTCPCollector(dir string) (*NetnsTCPCollector, error) {
	c := &NetnsTCPCollector{dir: dir}
	if _, err := c.read(); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *NetnsTCPCollector) Describe(ch chan<- *prometheus.Desc) {
	for _, m := range netnsTCPCounters {
		ch <- m.desc
	}
}

func (c *NetnsTCPCollector) Collect(ch chan<- prometheus.Metric) {
	values, err := c.read()
	if err != nil {
		for _, m := range netnsTCPCounters {
			ch <- prometheus.NewInvalidMetric(m.desc, err)
		}
		return
	}
	for _, m := range netnsTCPCounters {
		// A field the running kernel does not have is left out rather
		// than reported as 0.
		if v, ok := values[m.file][m.proto+"."+m.field]; ok {
			ch <- prometheus.MustNewConstMetric(m.desc, prometheus.CounterValue, v*m.scale)
		}
	}
}

// read returns file -> "Proto.Field" -> value.
func (c *NetnsTCPCollector) read() (map[string]map[string]float64, error) {
	out := map[string]map[string]float64{}
	for _, f := range []string{"netstat", "snmp"} {
		data, err := os.ReadFile(filepath.Join(c.dir, f))
		if err != nil {
			return nil, errors.Wrap(err, "failed to read TCP counters")
		}
		v, err := parseProcNetCounters(data)
		if err != nil {
			return nil, errors.Wrapf(err, "failed to parse %v", f)
		}
		out[f] = v
	}
	return out, nil
}

// parseProcNetCounters parses the /proc/net/{netstat,snmp} layout: per
// protocol a line of field names and a line of values, both prefixed with
// "Proto:". (prometheus/procfs declares TCPMemoryPressures and
// TCPMemoryPressuresChrono but, as of v0.19.2, never fills them in.)
func parseProcNetCounters(data []byte) (map[string]float64, error) {
	out := map[string]float64{}
	s := bufio.NewScanner(bytes.NewReader(data))
	for s.Scan() {
		names := strings.Fields(s.Text())
		if len(names) == 0 {
			continue
		}
		if !s.Scan() {
			return nil, errors.Errorf("%v has no value line", names[0])
		}
		values := strings.Fields(s.Text())
		if len(values) != len(names) || values[0] != names[0] {
			return nil, errors.Errorf("%v: %v names, %v values", names[0], len(names), len(values))
		}
		proto := strings.TrimSuffix(names[0], ":")
		for i := 1; i < len(names); i++ {
			v, err := strconv.ParseFloat(values[i], 64)
			if err != nil {
				return nil, errors.Wrapf(err, "%v.%v", proto, names[i])
			}
			out[proto+"."+names[i]] = v
		}
	}
	return out, s.Err()
}

// registerNetnsTCPCollector adds the collector to the default registry, or
// says once why the counters are not exported.
func registerNetnsTCPCollector(dir string) {
	c, err := NewNetnsTCPCollector(dir)
	if err != nil {
		logrus.WithError(err).Info("network namespace TCP counters are not exported (netns_tcp_*)")
		return
	}
	if err := prometheus.Register(c); err != nil {
		if _, ok := err.(prometheus.AlreadyRegisteredError); !ok {
			logrus.WithError(err).Warn("failed to register network namespace TCP counters")
		}
	}
}
