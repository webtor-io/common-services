package services

import (
	"fmt"
	"net"
	"net/http"

	"github.com/pkg/errors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/sirupsen/logrus"
	"github.com/urfave/cli"
)

const (
	promHostFlag = "prom-host"
	promHostPort = "prom-port"
)

type Prom struct {
	host string
	port int
	ln   net.Listener
}

func NewProm(c *cli.Context) *Prom {
	return &Prom{
		host: c.String(promHostFlag),
		port: c.Int(promHostPort),
	}
}

func RegisterPromFlags(f []cli.Flag) []cli.Flag {
	return append(f,
		cli.StringFlag{
			Name:  promHostFlag,
			Usage: "prometheus metrics listening host",
			Value: "",
		},
		cli.IntFlag{
			Name:  promHostPort,
			Usage: "prometheus metrics listening port",
			Value: 8083,
		})
}

func (s *Prom) Serve() error {
	addr := fmt.Sprintf("%s:%d", s.host, s.port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return errors.Wrap(err, "Failed to web listen to tcp connection")
	}
	s.ln = ln
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())
	logrus.Infof("Serving Prom Metrics at %v", addr)
	return http.Serve(ln, mux)
}

func (s *Prom) Close() {
	if s.ln != nil {
		s.ln.Close()
	}
}
