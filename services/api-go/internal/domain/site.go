// Defines the site-level operational rollup.
package domain

import "time"

// Site carries provisioned metadata and operational rollups for one site.
type Site struct {
	SiteID         string
	Name           string
	Country        *string
	Region         *string
	GridOperatorID *string
	Lat            *float64
	Lng            *float64
	Status         string
	AssetCount     int64
	LastSeenAt     *time.Time
	OpenAlerts     AlertCounts
}

type AlertCounts struct {
	Total    int64
	Critical int64
	Warning  int64
	Info     int64
}
