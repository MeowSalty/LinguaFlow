package execution

import (
	"encoding/json"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/repair"
)

func TestResolveFreezesRetryReminderAndRestoreRequiresIt(t *testing.T) {
	spec, err := Resolve(validExecutionInput())
	if err != nil {
		t.Fatal(err)
	}
	if spec.RetryReminderTemplate != repair.DefaultRetryReminderTemplate {
		t.Fatal("new execution did not materialize reminder")
	}
	spec.RetryReminderTemplate = "saved reminder {{.Reason}}"
	stored, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	var restored ResolvedExecutionSpec
	if err := json.Unmarshal(stored, &restored); err != nil {
		t.Fatal(err)
	}
	if err := ValidateSpec(&restored); err != nil {
		t.Fatal(err)
	}
	if restored.RetryReminderTemplate != "saved reminder {{.Reason}}" {
		t.Fatal("restore changed reminder")
	}
	restored.RetryReminderTemplate = ""
	if err := ValidateSpec(&restored); err == nil {
		t.Fatal("restore silently selected a new reminder")
	}
}
