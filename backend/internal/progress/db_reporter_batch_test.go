package progress

import (
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/event"
)

func TestDBReporter_OnBatchEvent_TypeAndLevel(t *testing.T) {
	broker := event.NewBroker(event.NewRingBufferStore(event.DefaultRingBufferConfig()))
	ch := broker.Subscribe(1)
	defer broker.Unsubscribe(1, ch)

	r := &DBReporter{broker: broker, jobID: 1}
	r.OnBatchEvent(BatchEvent{
		Stage:        "translate",
		SegmentIDs:   []string{"0"},
		SegmentCount: 1,
		Status:       "failed",
		ErrorType:    "backend_error",
		ErrorMessage: "rate limited",
		HTTPStatus:   429,
		SentContent:  "user prompt",
	})

	var evt event.Event
	select {
	case evt = <-ch:
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for batch event")
	}
	if evt.Type != "batch" {
		t.Fatalf("type=%q want batch", evt.Type)
	}
	if evt.Level != "error" {
		t.Fatalf("level=%q want error", evt.Level)
	}
	md := evt.Metadata
	if md["http_status"] != 429 {
		t.Fatalf("http_status=%v want 429", md["http_status"])
	}
	if md["sent_length"] != len("user prompt") {
		t.Fatalf("sent_length=%v", md["sent_length"])
	}
}

func TestDBReporter_OnBatchEvent_Truncation(t *testing.T) {
	broker := event.NewBroker(event.NewRingBufferStore(event.DefaultRingBufferConfig()))
	ch := broker.Subscribe(2)
	defer broker.Unsubscribe(2, ch)

	big := make([]byte, MaxSSEBatchContentBytes+1)
	for i := range big {
		big[i] = 'a'
	}
	r := &DBReporter{broker: broker, jobID: 2}
	r.OnBatchEvent(BatchEvent{
		Status:          "success",
		SegmentCount:    1,
		ReceivedContent: string(big),
	})

	var evt event.Event
	select {
	case evt = <-ch:
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for batch event")
	}
	md := evt.Metadata
	if md["received_truncated"] != true {
		t.Fatal("expected received_truncated")
	}
	if len(md["received_content"].(string)) != MaxSSEBatchContentBytes {
		t.Fatalf("received_content len=%d", len(md["received_content"].(string)))
	}
}

func TestDBReporter_OnBatchEvent_TruncationSignals(t *testing.T) {
	broker := event.NewBroker(event.NewRingBufferStore(event.DefaultRingBufferConfig()))
	ch := broker.Subscribe(3)
	defer broker.Unsubscribe(3, ch)

	r := &DBReporter{broker: broker, jobID: 3}
	r.OnBatchEvent(BatchEvent{
		Status:       "success",
		SegmentCount: 1,
		Truncated:    true,
		Repaired:     []string{"json.close-braces", "json.truncation-salvage"},
	})

	var evt event.Event
	select {
	case evt = <-ch:
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for batch event")
	}
	md := evt.Metadata
	if md["truncated"] != true {
		t.Fatalf("truncated=%v want true", md["truncated"])
	}
	repaired, ok := md["repaired"].([]string)
	if !ok || len(repaired) != 2 || repaired[0] != "json.close-braces" || repaired[1] != "json.truncation-salvage" {
		t.Fatalf("repaired=%v want [json.close-braces json.truncation-salvage]", md["repaired"])
	}
}

func TestDBReporter_OnBatchEvent_RepairedOmittedWhenEmpty(t *testing.T) {
	broker := event.NewBroker(event.NewRingBufferStore(event.DefaultRingBufferConfig()))
	ch := broker.Subscribe(4)
	defer broker.Unsubscribe(4, ch)

	r := &DBReporter{broker: broker, jobID: 4}
	r.OnBatchEvent(BatchEvent{Status: "success", SegmentCount: 1})

	var evt event.Event
	select {
	case evt = <-ch:
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for batch event")
	}
	md := evt.Metadata
	if md["truncated"] != false {
		t.Fatalf("truncated=%v want false (unconditional, like shrink_attempted)", md["truncated"])
	}
	if _, ok := md["repaired"]; ok {
		t.Fatalf("repaired=%v want absent when empty", md["repaired"])
	}
}
