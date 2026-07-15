package netcup

import (
	"net/http"
	"testing"
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
