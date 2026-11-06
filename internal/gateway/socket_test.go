package gateway

import "testing"

func TestFrameMessageLimit(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name string
		wire []byte
		ok   bool
	}{
		{name: "text", wire: []byte{0x81, 2, 'o', 'k'}, ok: true},
		{name: "binary", wire: []byte{0x82, 2, 0, 255}, ok: true},
		{name: "fragmented", wire: []byte{0x01, 2, 'o', 'k', 0x80, 2, 'o', 'k'}, ok: true},
		{name: "ping in fragmented", wire: []byte{0x01, 2, 'o', 'k', 0x89, 0, 0x80, 2, 'o', 'k'}, ok: true},
		{name: "frame exceeds limit", wire: []byte{0x81, 126, 0, 5}},
		{name: "fragments exceed limit", wire: []byte{0x01, 3, 'a', 'b', 'c', 0x80, 2}},
		{name: "continuation without message", wire: []byte{0x80, 0}},
		{name: "unnegotiated compression", wire: []byte{0xc1, 0}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			g := frameGuard{limit: 4}
			var err error
			// Splitting at every byte verifies incremental header parsing.
			for _, b := range tt.wire {
				if err = g.feed([]byte{b}); err != nil {
					break
				}
			}
			if (err == nil) != tt.ok {
				t.Fatalf("guard = %v, want success %v", err, tt.ok)
			}
		})
	}
}
