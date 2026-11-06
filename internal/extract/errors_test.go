package extract

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

func TestFailureClassNeverIncludesProviderDetails(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want string
	}{
		{context.DeadlineExceeded, "timeout"},
		{fmt.Errorf("extraction timeout or cancellation: %w", context.DeadlineExceeded), "timeout"},
		{errors.New("extraction HTTP status 429 (body redacted)"), "provider_http_429"},
		{errors.New("extraction credential is not configured"), "no_key"},
		{errors.New("extraction HTTP request failed (details redacted)"), "provider_network"},
		{errors.New("invalid extraction response (body redacted)"), "parse"},
		{errors.New("capture session binding changed, start a new session"), "scope"},
		{errors.New("unexpected synthetic private text"), "capture"},
	} {
		if got := FailureClass(tc.err); got != tc.want {
			t.Fatalf("class=%s want=%s", got, tc.want)
		}
	}
}
