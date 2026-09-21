package domain

import "time"

// IdempotencyRecord is the persistence row for an idempotency key. The
// handler stores a snapshot of the created task (ResponseBody) so that a
// replayed request returns a byte-for-byte identical response.
//
// ResponseBody holds the MARSHALLED task only (not the envelope); the handler
// wraps it in {"data": <raw>} on both the fresh and replay paths, which is
// what guarantees the two responses are byte-identical.
type IdempotencyRecord struct {
	Key            string    `db:"key"`
	UserID         string    `db:"user_id"`
	TaskID         string    `db:"task_id"`
	ResponseStatus int       `db:"response_status"`
	ResponseBody   string    `db:"response_body"`
	CreatedAt      time.Time `db:"created_at"`
	ExpiresAt      time.Time `db:"expires_at"`
}
