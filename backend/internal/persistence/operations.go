package persistence

// Operation is bounded telemetry vocabulary shared by producers and aggregation.
type Operation string

const (
	ResponseCapture     Operation = "response_capture"
	OutputArchive       Operation = "output_archive"
	BusinessPublication Operation = "business_publication"
	MemberPromotion     Operation = "member_promotion"
)

func RegisteredOperation(op string) bool {
	switch Operation(op) {
	case ResponseCapture, OutputArchive, BusinessPublication, MemberPromotion:
		return true
	}
	switch op {
	case "reserve", "seal", "handoff", "checkpoint", "stage_initialize", "pool_advance", "abort", "sent", "received", "failed", "completed", "candidate", "commit", "retire", "main_failure":
		return true
	}
	return false
}
