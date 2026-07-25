package backend

import "testing"

func TestParseThinkingLevel(t *testing.T) {
	tests := []struct {
		name    string
		opts    map[string]any
		want    ThinkingLevel
		wantErr bool
	}{
		{"missing", map[string]any{}, ThinkingOff, false},
		{"nil map key absent", nil, ThinkingOff, false},
		{"empty string", map[string]any{"thinking_level": ""}, ThinkingOff, false},
		{"off", map[string]any{"thinking_level": "off"}, ThinkingOff, false},
		{"low", map[string]any{"thinking_level": "low"}, ThinkingLow, false},
		{"medium", map[string]any{"thinking_level": "medium"}, ThinkingMedium, false},
		{"high", map[string]any{"thinking_level": "high"}, ThinkingHigh, false},
		{"invalid", map[string]any{"thinking_level": "xhigh"}, "", true},
		{"wrong type", map[string]any{"thinking_level": 1}, "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseThinkingLevel(tt.opts)
			if tt.wantErr {
				if err == nil {
					t.Fatal("want error")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Fatalf("got %q want %q", got, tt.want)
			}
		})
	}
}

func TestThinkingLevel_Enabled(t *testing.T) {
	if ThinkingOff.Enabled() {
		t.Fatal("off should not be enabled")
	}
	if ThinkingLevel("").Enabled() {
		t.Fatal("empty should not be enabled")
	}
	if !ThinkingLow.Enabled() || !ThinkingMedium.Enabled() || !ThinkingHigh.Enabled() {
		t.Fatal("low/medium/high should be enabled")
	}
}
