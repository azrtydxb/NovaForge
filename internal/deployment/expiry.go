package deployment

import "time"

// PostgreSQL timestamps have microsecond precision. Round down, never up across
// an authority boundary; the same value must bind approval and issuer replay.
func earlierExpiry(current *time.Time, candidate time.Time) *time.Time {
	if candidate.IsZero() {
		return current
	}
	candidate = candidate.UTC().Truncate(time.Microsecond)
	if current == nil || candidate.Before(*current) {
		return &candidate
	}
	return current
}

func credentialDeadline(op Operation) time.Time {
	if len(op.Attempts) == 0 {
		return time.Time{}
	}
	a := op.Attempts[len(op.Attempts)-1]
	if a.CredentialExpiresAt == nil {
		return time.Time{}
	}
	return a.CredentialExpiresAt.UTC()
}

func coversExecution(expiry time.Time) bool {
	return !expiry.Before(time.Now().Add(5 * time.Minute))
}
