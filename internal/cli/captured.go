package cli

import (
	"context"
	"time"
)

// What `stagd preview mail` and `stagd preview texts` share: waiting on
// the server (WaitForMail, WaitForTexts) for what arrives after a cursor.

// longestWait is the longest one call may wait; the server holds it open meanwhile.
const longestWait = time.Minute

// waitAfter asks the server, a minute at a time, for what matches after the
// cursor until something does or the timeout passes: none, then.
func waitAfter[T any](ctx context.Context, timeout time.Duration, after string, wait func(ctx context.Context, after string, d time.Duration) ([]T, string, error)) ([]T, string, error) {
	deadline := time.Now().Add(timeout)

	for {
		left := time.Until(deadline)
		if left < 0 {
			left = 0
		}

		d := min(left, longestWait)

		// Room for the answer past the server's wait.
		callCtx, cancel := context.WithTimeout(ctx, d+30*time.Second)
		found, cursor, err := wait(callCtx, after, d)

		cancel()

		if err != nil {
			return nil, after, err
		}

		after = cursor

		if len(found) > 0 || time.Until(deadline) <= 0 {
			return found, after, nil
		}
	}
}
