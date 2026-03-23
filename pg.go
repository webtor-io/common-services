package services

import (
	"crypto/tls"
	"fmt"
	"sync"
	"time"

	"github.com/go-pg/pg/v10"
	"github.com/urfave/cli"
)

const (
	pgHostFlag     = "postgres-host"
	pgPortFlag     = "postgres-port"
	pgUserFlag     = "postgres-user"
	pgPasswordFlag = "postgres-password"
	pgDatabaseFlag = "postgres-database"
	pgSSLFlag          = "postgres-ssl"
	pgPoolSizeFlag     = "postgres-pool-size"
	pgMinIdleConnsFlag = "postgres-min-idle-conns"
	pgMaxConnAgeFlag       = "postgres-max-conn-age"
	pgIdleTimeoutFlag      = "postgres-idle-timeout"
	pgMaxRetriesFlag       = "postgres-max-retries"
	pgMinRetryBackoffFlag  = "postgres-min-retry-backoff"
	pgMaxRetryBackoffFlag  = "postgres-max-retry-backoff"
)

func RegisterPGFlags(f []cli.Flag) []cli.Flag {
	return append(f,
		cli.StringFlag{
			Name:   pgHostFlag,
			Usage:  "postgres host",
			Value:  "",
			EnvVar: "PG_HOST",
		},
		cli.IntFlag{
			Name:   pgPortFlag,
			Usage:  "postgres port",
			Value:  5432,
			EnvVar: "PG_PORT",
		},
		cli.StringFlag{
			Name:   pgUserFlag,
			Usage:  "postgres user",
			Value:  "",
			EnvVar: "PG_USER",
		},
		cli.StringFlag{
			Name:   pgPasswordFlag,
			Usage:  "postgres password",
			Value:  "",
			EnvVar: "PG_PASSWORD",
		},
		cli.StringFlag{
			Name:   pgDatabaseFlag,
			Usage:  "postgres database",
			Value:  "",
			EnvVar: "PG_DATABASE",
		},
		cli.BoolFlag{
			Name:   pgSSLFlag,
			Usage:  "postgres ssl",
			EnvVar: "PG_SSL",
		},
		cli.IntFlag{
			Name:   pgPoolSizeFlag,
			Usage:  "postgres pool size",
			Value:  5,
			EnvVar: "PG_POOL_SIZE",
		},
		cli.IntFlag{
			Name:   pgMinIdleConnsFlag,
			Usage:  "postgres min idle connections",
			Value:  1,
			EnvVar: "PG_MIN_IDLE_CONNS",
		},
		cli.StringFlag{
			Name:   pgMaxConnAgeFlag,
			Usage:  "postgres max connection age (0 = no limit)",
			Value:  "0",
			EnvVar: "PG_MAX_CONN_AGE",
		},
		cli.StringFlag{
			Name:   pgIdleTimeoutFlag,
			Usage:  "postgres idle timeout (0 = no limit)",
			Value:  "0",
			EnvVar: "PG_IDLE_TIMEOUT",
		},
		cli.IntFlag{
			Name:   pgMaxRetriesFlag,
			Usage:  "postgres max retries on transient errors (EOF, timeout)",
			Value:  3,
			EnvVar: "PG_MAX_RETRIES",
		},
		cli.StringFlag{
			Name:   pgMinRetryBackoffFlag,
			Usage:  "postgres min retry backoff",
			Value:  "100ms",
			EnvVar: "PG_MIN_RETRY_BACKOFF",
		},
		cli.StringFlag{
			Name:   pgMaxRetryBackoffFlag,
			Usage:  "postgres max retry backoff",
			Value:  "1s",
			EnvVar: "PG_MAX_RETRY_BACKOFF",
		},
	)
}

type PG struct {
	host         string
	port         int
	user         string
	password     string
	database     string
	ssl          bool
	poolSize     int
	minIdleConns int
	maxConnAge      time.Duration
	idleTimeout     time.Duration
	maxRetries      int
	minRetryBackoff time.Duration
	maxRetryBackoff time.Duration
	db              *pg.DB
	mux          sync.Mutex
	inited       bool
}

func NewPG(c *cli.Context) *PG {
	maxConnAge, _ := time.ParseDuration(c.String(pgMaxConnAgeFlag))
	idleTimeout, _ := time.ParseDuration(c.String(pgIdleTimeoutFlag))
	minRetryBackoff, _ := time.ParseDuration(c.String(pgMinRetryBackoffFlag))
	maxRetryBackoff, _ := time.ParseDuration(c.String(pgMaxRetryBackoffFlag))
	return &PG{
		host:            c.String(pgHostFlag),
		port:            c.Int(pgPortFlag),
		user:            c.String(pgUserFlag),
		password:        c.String(pgPasswordFlag),
		database:        c.String(pgDatabaseFlag),
		ssl:             c.Bool(pgSSLFlag),
		poolSize:        c.Int(pgPoolSizeFlag),
		minIdleConns:    c.Int(pgMinIdleConnsFlag),
		maxConnAge:      maxConnAge,
		idleTimeout:     idleTimeout,
		maxRetries:      c.Int(pgMaxRetriesFlag),
		minRetryBackoff: minRetryBackoff,
		maxRetryBackoff: maxRetryBackoff,
	}
}

func (s *PG) get() *pg.DB {
	if s.host == "" {
		return nil
	}
	opts := &pg.Options{}
	opts.Addr = fmt.Sprintf("%v:%v", s.host, s.port)
	opts.User = s.user
	opts.Password = s.password
	opts.Database = s.database
	opts.PoolSize = s.poolSize
	opts.MinIdleConns = s.minIdleConns
	opts.MaxConnAge = s.maxConnAge
	opts.IdleTimeout = s.idleTimeout
	opts.MaxRetries = s.maxRetries
	opts.MinRetryBackoff = s.minRetryBackoff
	opts.MaxRetryBackoff = s.maxRetryBackoff
	if s.ssl {
		opts.TLSConfig = &tls.Config{
			InsecureSkipVerify: true,
		}
	}
	return pg.Connect(opts)
}

func (s *PG) Get() *pg.DB {
	s.mux.Lock()
	defer s.mux.Unlock()
	if s.inited {
		return s.db
	}
	s.db = s.get()
	s.inited = true
	return s.db
}

func (s *PG) Close() {
	if s.db != nil {
		s.db.Close()
	}
}
