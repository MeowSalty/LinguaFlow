package backend

import "testing"

func TestParseThinking(t *testing.T) {
	tests := []struct {
		name    string
		opts    map[string]any
		want    Thinking
		wantErr bool
	}{
		{"missing", map[string]any{}, Thinking{}, false},
		{"nil map key absent", nil, Thinking{}, false},
		{"nil value", map[string]any{"thinking_level": nil}, Thinking{}, false},
		{"empty string", map[string]any{"thinking_level": ""}, Thinking{}, false},
		{"off", map[string]any{"thinking_level": "off"}, Thinking{Level: ThinkingOff, Set: true}, false},
		{"minimal", map[string]any{"thinking_level": "minimal"}, Thinking{Level: ThinkingMinimal, Set: true}, false},
		{"low", map[string]any{"thinking_level": "low"}, Thinking{Level: ThinkingLow, Set: true}, false},
		{"medium", map[string]any{"thinking_level": "medium"}, Thinking{Level: ThinkingMedium, Set: true}, false},
		{"high", map[string]any{"thinking_level": "high"}, Thinking{Level: ThinkingHigh, Set: true}, false},
		{"invalid", map[string]any{"thinking_level": "xhigh"}, Thinking{}, true},
		{"wrong type", map[string]any{"thinking_level": 1}, Thinking{}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseThinking(tt.opts)
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
				t.Fatalf("got %+v want %+v", got, tt.want)
			}
		})
	}
}

func TestThinking_Active(t *testing.T) {
	if (Thinking{Level: ThinkingOff, Set: true}).Active() {
		t.Fatal("explicit off should not be active")
	}
	if (Thinking{Level: ThinkingLow}).Active() {
		t.Fatal("unset switch should not be active")
	}
	if !(Thinking{Level: ThinkingLow, Set: true}).Active() {
		t.Fatal("low with switch on should be active")
	}
	if !(Thinking{Level: ThinkingMedium, Set: true}).Active() {
		t.Fatal("medium with switch on should be active")
	}
	if !(Thinking{Level: ThinkingHigh, Set: true}).Active() {
		t.Fatal("high with switch on should be active")
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
