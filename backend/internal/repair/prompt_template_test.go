package repair

import (
	"errors"
	"strings"
	"testing"
)

func TestRetryReminderTemplateUsesFrozenContent(t *testing.T) {
	got, err := RenderRetryReminder("saved {{.MissingIDs}} {{.Reason}} {{.PreviousHead}}", []string{"1", "3"}, errors.New("bad JSON"), strings.Repeat("x", 201))
	if err != nil {
		t.Fatal(err)
	}
	if got != "saved 1, 3 bad JSON "+strings.Repeat("x", 200)+"…" {
		t.Fatalf("unexpected rendered template: %q", got)
	}
	for _, template := range []string{"", "{{", "{{.Unsupported}}"} {
		if _, err := RenderRetryReminder(template, nil, nil, ""); err == nil {
			t.Fatalf("accepted invalid template %q", template)
		}
	}
}
