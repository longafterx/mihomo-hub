package analytics_test

import (
	"errors"
	"net/url"
	"testing"
	"time"

	"github.com/longafterx/mihomo-hub/internal/analytics"
)

func TestQueryBounds(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	for _, tt := range []struct {
		name, kind, query string
		valid             bool
	}{
		{"defaults", "summary", "", true},
		{"UTC hours", "traffic", "from=2026-11-01T00:00:00Z&to=2026-11-02T00:00:00Z&step=1h", true},
		{"dimension", "top", "dimension=host", true},
		{"injection", "top", "dimension=host%3BDROP+TABLE+instances", false},
		{"huge export", "connections", "limit=10000", false},
		{"deep page", "gaps", "offset=100001", false},
		{"duplicate", "traffic", "step=1h&step=1m", false},
		{"non UTC", "summary", "from=2026-11-05T00:00:00%2B08:00", false},
		{"unaligned", "summary", "from=2026-11-05T00:00:01Z", false},
		{"too many points", "traffic", "from=2026-11-01T00:00:00Z&step=1m", false},
		{"bad UUID", "summary", "instance_ids=arbitrary", false},
		{"unknown param", "summary", "target=http://evil", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			v, err := url.ParseQuery(tt.query)
			if err != nil {
				t.Fatal(err)
			}
			_, err = analytics.Parse(tt.kind, v, now)
			if (err == nil) != tt.valid {
				t.Fatal(err)
			}
			if err != nil && !errors.Is(err, analytics.ErrInvalid) {
				t.Fatal(err)
			}
		})
	}
}
