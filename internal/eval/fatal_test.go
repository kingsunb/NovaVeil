package eval

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestIsFatalEvalStatus(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"401 unauthorized", errors.New("request failed with status 401"), true},
		{"403 forbidden", errors.New("upstream responded 403 Forbidden"), true},
		{"404 not found", errors.New("request failed with status 404"), true},
		{"429 rate limit", errors.New("request failed with status 429"), false},
		{"400 bad request", errors.New("upstream responded 400 Bad Request"), false},
		{"500 internal", errors.New("request failed with status 500"), false},
		{"502 bad gateway", errors.New("upstream responded 502 Bad Gateway"), false},
		{"network no status", errors.New("dial tcp: connection refused"), false},
		{"timeout no status", errors.New("context deadline exceeded"), false},
		{"nil", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, isFatalEvalStatus(tc.err))
		})
	}
}
