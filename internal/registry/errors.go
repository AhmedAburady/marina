package registry

import "errors"

// Sentinel errors returned by CheckUpdate when the registry HEAD request fails
// with a well-known HTTP status code. Callers should use errors.Is to detect
// specific failure modes rather than string-matching err.Error().
var (
	// ErrRateLimited is returned when the registry responds with HTTP 429
	// (Too Many Requests). The caller may surface this as "rate limited".
	ErrRateLimited = errors.New("registry: rate limited")
)
