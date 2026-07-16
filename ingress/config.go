package ingress

import (
	"os"
	"strconv"
	"time"
)

type Config struct {
	MaxConcurrent   int
	ReclaimInterval time.Duration
	ReclaimMinIdle  time.Duration
	MaxDeliveries   int64
	ReclaimBatch    int64
}

func LoadConfig() Config {
	return Config{
		MaxConcurrent:   envInt("AVM_MAX_CONCURRENT", 4),
		ReclaimInterval: envDuration("AVM_RECLAIM_INTERVAL", 30*time.Second),
		ReclaimMinIdle:  envDuration("AVM_RECLAIM_MIN_IDLE", 60*time.Second),
		MaxDeliveries:   int64(envInt("AVM_MAX_DELIVERIES", 5)),
		ReclaimBatch:    int64(envInt("AVM_RECLAIM_BATCH", 32)),
	}
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return def
}

func envDuration(key string, def time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return def
}
