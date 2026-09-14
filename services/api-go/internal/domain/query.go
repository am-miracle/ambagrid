// defines repository query shapes and shared lookup errors.
package domain

import "errors"

// ErrNotFound marks a missing domain object.
var ErrNotFound = errors.New("not found")

var ErrInvalidData = errors.New("invalid data")

// Query carries filters and pagination for a repository listing.
type Query[F any] struct {
	Filter F
	Limit  int
	Cursor string
}

type AssetQuery = Query[AssetFilter]

type AlertQuery = Query[AlertFilter]

type SiteQuery struct {
	Limit  int
	Cursor string
}
