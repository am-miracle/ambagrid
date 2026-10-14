package domain

import "errors"

var (
	ErrIdentityMismatch  = errors.New("queue belongs to a different edge identity")
	ErrCriticalReserve   = errors.New("queue capacity is reserved for critical events")
	ErrStorageCapacity   = errors.New("queue storage capacity reached")
	ErrFilesystemReserve = errors.New("filesystem free-space reserve reached")
	ErrEventTooLarge     = errors.New("event exceeds the configured size limit")
	ErrEventNotFound     = errors.New("queue event not found")
	ErrInvalidTransition = errors.New("invalid queue event status transition")
)
