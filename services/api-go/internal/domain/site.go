// Defines the site-level operational rollup.
package domain

import "time"

// Site summarizes assets and open alerts for one site.
type Site struct {
	SiteID     string
	AssetCount int64
	LastSeenAt *time.Time
	OpenAlerts AlertCounts
}

type AlertCounts struct {
	Total    int64
	Critical int64
	Warning  int64
	Info     int64
}
