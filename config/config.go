package config

import (
	"fmt"
	"time"

	"github.com/spf13/viper"
)

type MongoConfig struct {
	URI string `mapstructure:"uri"`
	DB  string `mapstructure:"db"`
}

type TaConfig struct {
	LogPattern []string `mapstructure:"logPattern"`
}

type TailConfig struct {
	RescanSeconds int `mapstructure:"rescanSeconds"`
}

type BatchConfig struct {
	Size            int `mapstructure:"size"`
	WorkerCount     int `mapstructure:"workerCount"`
	FlushIntervalMs int `mapstructure:"flushIntervalMs"`
}

type RetryConfig struct {
	MaxElapsedTime time.Duration `mapstructure:"maxElapsedTime"`
}

type LogConfig struct {
	Level string `mapstructure:"level"`
}

type Config struct {
	Mongo MongoConfig `mapstructure:"mongo"`
	Ta    TaConfig    `mapstructure:"ta"`
	Tail  TailConfig  `mapstructure:"tail"`
	Batch BatchConfig `mapstructure:"batch"`
	Retry RetryConfig `mapstructure:"retry"`
	Log   LogConfig   `mapstructure:"log"`
}

func DefaultConfig() Config {
	cfg := Config{
		Mongo: MongoConfig{
			DB: "ta2mongo",
		},
		Ta: TaConfig{
			LogPattern: []string{"/mnt/shared-data-log/ta\\.production-.*"}, // regex
		},
		Tail: TailConfig{
			RescanSeconds: 30,
		},
		Batch: BatchConfig{
			Size:            1000,
			WorkerCount:     2,
			FlushIntervalMs: 1000,
		},
		Retry: RetryConfig{
			MaxElapsedTime: 10 * time.Second,
		},
		Log: LogConfig{
			Level: "info",
		},
	}
	return cfg
}

func LoadConfig(v *viper.Viper) (Config, error) {
	cfg := DefaultConfig()

	if v.IsSet("mongo.uri") {
		cfg.Mongo.URI = v.GetString("mongo.uri")
	}
	if v.IsSet("mongo.db") {
		cfg.Mongo.DB = v.GetString("mongo.db")
	}

	if v.IsSet("ta.logPattern") {
		cfg.Ta.LogPattern = v.GetStringSlice("ta.logPattern")
	}

	// tail.rescan 一定会开启：只需要配置 rescanSeconds
	if v.IsSet("tail.rescanSeconds") {
		cfg.Tail.RescanSeconds = v.GetInt("tail.rescanSeconds")
	}

	if v.IsSet("batch.size") {
		cfg.Batch.Size = v.GetInt("batch.size")
	}
	if v.IsSet("batch.workerCount") {
		cfg.Batch.WorkerCount = v.GetInt("batch.workerCount")
	}
	if v.IsSet("batch.flushIntervalMs") {
		cfg.Batch.FlushIntervalMs = v.GetInt("batch.flushIntervalMs")
	}

	if v.IsSet("retry.maxElapsedTime") {
		cfg.Retry.MaxElapsedTime = v.GetDuration("retry.maxElapsedTime")
	}

	if v.IsSet("log.level") {
		cfg.Log.Level = v.GetString("log.level")
	}

	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (c *Config) Validate() error {
	if c.Mongo.URI == "" {
		return fmt.Errorf("mongo.uri is required")
	}
	if c.Mongo.DB == "" {
		return fmt.Errorf("mongo.db is required")
	}
	if len(c.Ta.LogPattern) == 0 {
		return fmt.Errorf("ta.logPattern is required")
	}
	if c.Tail.RescanSeconds <= 0 {
		return fmt.Errorf("tail.rescanSeconds must be > 0")
	}

	// Safety nets (even though defaults exist)
	if c.Batch.Size <= 0 {
		c.Batch.Size = 1000
	}
	if c.Batch.WorkerCount <= 0 {
		c.Batch.WorkerCount = 2
	}
	if c.Batch.FlushIntervalMs <= 0 {
		c.Batch.FlushIntervalMs = 1000
	}

	if c.Retry.MaxElapsedTime <= 0 {
		c.Retry.MaxElapsedTime = 10 * time.Second
	}

	if c.Log.Level == "" {
		c.Log.Level = "info"
	}
	return nil
}
