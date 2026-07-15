package netcup

import (
	"net/http"
	"testing"
	"time"
)

func TestClassifyHTTPError(t *testing.T) {
	tests := []struct {
		status int
		class  ErrorClass
	}{
		{http.StatusTooManyRequests, ErrorClassRateLimited},
		{http.StatusUnauthorized, ErrorClassAuth},
		{http.StatusBadRequest, ErrorClassValidation},
		{http.StatusBadGateway, ErrorClassTransient},
	}
	for _, tt := range tests {
		if got := classifyHTTPError(tt.status).Class; got != tt.class {
			t.Errorf("classifyHTTPError(%d) = %q, want %q", tt.status, got, tt.class)
		}
	}
}

func TestParseRetryAfter(t *testing.T) {
	now := time.Unix(100, 0)
	if got := parseRetryAfter("7", now); got != 7*time.Second {
		t.Fatalf("seconds retry-after = %s", got)
	}
	if got := parseRetryAfter("invalid", now); got != 0 {
		t.Fatalf("invalid retry-after = %s, want zero", got)
	}
}
