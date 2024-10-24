package services

import (
	"fmt"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/urfave/cli"

	log "github.com/sirupsen/logrus"
)

// RedisClient makes Redis Client from cli and environment variables
// Automatically hanldles Sentinel configuration
type RedisClient struct {
	host               string
	port               int
	pass               string
	user               string
	sentinelPort       int
	sentinelMasterName string
	value              redis.UniversalClient
	inited             bool
	mux                sync.Mutex
}

const (
	redisHostFlag           = "redis-host"
	redisPortFlag           = "redis-port"
	redisPassFlag           = "redis-pass"
	redisUserFlag           = "redis-user"
	redisSentinelPortFlag   = "redis-sentinel-port"
	redisSentinelMasterName = "redis-sentinel-master-name"
)

// NewRedisClient initializes RedisClient
func NewRedisClient(c *cli.Context) *RedisClient {
	return &RedisClient{
		host:               c.String(redisHostFlag),
		port:               c.Int(redisPortFlag),
		pass:               c.String(redisPassFlag),
		user:               c.String(redisUserFlag),
		sentinelPort:       c.Int(redisSentinelPortFlag),
		sentinelMasterName: c.String(redisSentinelMasterName),
	}
}

// Close closes RedisClient
func (s *RedisClient) Close() {
	if s.value != nil {
		_ = s.value.Close()
	}
}

func (s *RedisClient) get() redis.UniversalClient {
	if s.sentinelPort != 0 {
		addrs := []string{fmt.Sprintf("%s:%d", s.host, s.sentinelPort)}
		log.Infof("using sentinel redis client with addrs=%v and master name=%v", addrs, s.sentinelMasterName)
		return redis.NewUniversalClient(&redis.UniversalOptions{
			Addrs:        addrs,
			Username:     s.user,
			Password:     s.pass,
			DB:           0,
			MasterName:   s.sentinelMasterName,
			DialTimeout:  30 * time.Second,
			ReadTimeout:  30 * time.Second,
			WriteTimeout: 30 * time.Second,
			MaxRetries:   10,
		})
	}
	addrs := []string{fmt.Sprintf("%s:%d", s.host, s.port)}
	log.Infof("using default redis client with addrs=%v", addrs)
	return redis.NewUniversalClient(&redis.UniversalOptions{
		Addrs:        addrs,
		Username:     s.user,
		Password:     s.pass,
		DB:           0,
		DialTimeout:  30 * time.Second,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
		MaxRetries:   10,
	})
}

// Get gets redis.UniversalCleint
func (s *RedisClient) Get() redis.UniversalClient {
	s.mux.Lock()
	defer s.mux.Unlock()
	if s.inited {
		return s.value
	}
	s.value = s.get()
	s.inited = true
	return s.value
}

// RegisterRedisClientFlags registers cli flags for RedisClient
func RegisterRedisClientFlags(f []cli.Flag) []cli.Flag {
	return append(f,
		cli.StringFlag{
			Name:   redisHostFlag,
			Usage:  "redis host",
			Value:  "localhost",
			EnvVar: "REDIS_MASTER_SERVICE_HOST, REDIS_SERVICE_HOST",
		},
		cli.IntFlag{
			Name:   redisPortFlag,
			Usage:  "redis port",
			Value:  6379,
			EnvVar: "REDIS_MASTER_SERVICE_PORT, REDIS_SERVICE_PORT",
		},
		cli.StringFlag{
			Name:   redisPassFlag,
			Usage:  "redis pass",
			Value:  "",
			EnvVar: "REDIS_PASS",
		},
		cli.StringFlag{
			Name:   redisUserFlag,
			Usage:  "redis user",
			Value:  "default",
			EnvVar: "REDIS_USER",
		},
		cli.IntFlag{
			Name:   redisSentinelPortFlag,
			Usage:  "redis sentinel port",
			EnvVar: "REDIS_SERVICE_PORT_REDIS_SENTINEL",
		},
		cli.StringFlag{
			Name:   redisSentinelMasterName,
			Usage:  "redis sentinel master name",
			Value:  "mymaster",
			EnvVar: "REDIS_SERVICE_SENTINEL_MASTER_NAME",
		},
	)
}
