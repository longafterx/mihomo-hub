//go:build integration

package integration_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"
)

func TestInstanceProbeLatency(t *testing.T) {
	h := setup(t)
	for _, id := range h.ids {
		path := "/api/v1/instances/" + id + "/test"
		response, _ := h.request(http.MethodPost, path, "", false)
		requireStatus(t, response, http.StatusUnauthorized)
		response, data := h.request(http.MethodPost, path, "", true)
		requireStatus(t, response, http.StatusOK)
		var result struct {
			Data struct {
				Reachable bool   `json:"reachable"`
				Version   string `json:"version"`
				LatencyMS *int64 `json:"latency_ms"`
			} `json:"data"`
		}
		if err := json.Unmarshal(data, &result); err != nil {
			t.Fatal(err)
		}
		if !result.Data.Reachable || result.Data.Version == "" {
			t.Fatal("successful probe missing version or reachability")
		}
		if result.Data.LatencyMS == nil || *result.Data.LatencyMS < 0 {
			t.Fatal("successful probe missing a non-negative server-measured latency")
		}
		for _, fake := range h.fakes {
			if bytes.Contains(data, []byte(fake.Secret)) {
				t.Fatal("probe leaked controller credentials")
			}
		}
	}
}
