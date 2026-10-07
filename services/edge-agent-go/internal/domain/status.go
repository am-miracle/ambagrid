package domain

type EventStatus string

const (
	StatusPersisted     EventStatus = "persisted"
	StatusPendingUpload EventStatus = "pending_upload"
	StatusUploaded      EventStatus = "uploaded"
	StatusAcknowledged  EventStatus = "acknowledged"
	StatusExpired       EventStatus = "expired"
)
