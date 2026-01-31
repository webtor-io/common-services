package services

import (
	"fmt"
	"sync"

	"github.com/nats-io/nats.go"
	log "github.com/sirupsen/logrus"
	"github.com/urfave/cli"
)

const (
	natsServiceHostFlag = "nats-service-host"
	natsServicePortFlag = "nats-service-port"
)

func RegisterNATSFlags(f []cli.Flag) []cli.Flag {
	return append(f,
		cli.StringFlag{
			Name:   natsServiceHostFlag,
			Usage:  "nats service host",
			Value:  "",
			EnvVar: "NATS_SERVICE_HOST",
		},
		cli.IntFlag{
			Name:   natsServicePortFlag,
			Usage:  "nats service port",
			Value:  4222,
			EnvVar: "NATS_SERVICE_PORT",
		},
	)
}

type NATS struct {
	host   string
	port   int
	nc     *nats.Conn
	mux    sync.Mutex
	inited bool
}

func NewNATS(c *cli.Context) *NATS {
	host := c.String(natsServiceHostFlag)
	if host == "" {
		return nil
	}
	return &NATS{
		host: host,
		port: c.Int(natsServicePortFlag),
	}
}

func (s *NATS) get() *nats.Conn {
	url := fmt.Sprintf("nats://%s:%d", s.host, s.port)
	nc, err := nats.Connect(url)
	if err != nil {
		log.WithError(err).Error("failed to connect to nats")
	}
	return nc
}

func (s *NATS) Get() *nats.Conn {
	s.mux.Lock()
	defer s.mux.Unlock()
	if s.inited {
		return s.nc
	}
	s.nc = s.get()
	s.inited = true
	return s.nc
}

func (s *NATS) Close() {
	if s.nc != nil {
		s.nc.Close()
	}
}
