package ingest

import "fmt"

type EventKey string

func newEventKey(siteID string, sequence uint64) EventKey {
	return EventKey(fmt.Sprintf("edge:%s:%d", siteID, sequence))
}

const (
	criticalBatteryOverheat = "BATTERY_OVERHEAT"
	criticalInverterFailure = "INVERTER_FAILURE"
	criticalTamperDetected  = "TAMPER_DETECTED"
	criticalSiteOutage      = "SITE_OUTAGE"
)

var criticalCodes = []string{
	criticalBatteryOverheat,
	criticalInverterFailure,
	criticalTamperDetected,
	criticalSiteOutage,
}
