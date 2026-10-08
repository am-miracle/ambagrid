package sqlite

import "errors"

type Config struct {
	Path                   string
	SiteID                 string
	GatewayID              string
	MaxStorageBytes        int64
	MaxEventBytes          int64
	WarningPercent         int
	CriticalReservePercent int
	MinFilesystemFreeBytes int64
}

func validateConfig(cfg Config) error {
	if cfg.Path == "" {
		return errors.New("queue path must not be empty")
	}
	if cfg.SiteID == "" {
		return errors.New("site ID must not be empty")
	}
	if cfg.GatewayID == "" {
		return errors.New("gateway ID must not be empty")
	}
	if cfg.MaxStorageBytes < 1<<20 {
		return errors.New("maximum storage must be at least 1 MiB")
	}
	if cfg.MaxEventBytes < 1 || cfg.MaxEventBytes > cfg.MaxStorageBytes*5/100 {
		return errors.New("maximum event size must be positive and no more than 5% of maximum storage")
	}
	if cfg.WarningPercent < 1 || cfg.WarningPercent >= 100 {
		return errors.New("warning percent must be between 1 and 99")
	}
	if cfg.CriticalReservePercent < 1 || cfg.CriticalReservePercent >= 100 {
		return errors.New("critical reserve percent must be between 1 and 99")
	}
	if cfg.WarningPercent >= 100-cfg.CriticalReservePercent {
		return errors.New("warning percent must be below the non-critical admission limit")
	}
	if cfg.MinFilesystemFreeBytes < 0 {
		return errors.New("minimum filesystem free bytes must not be negative")
	}
	return nil
}
