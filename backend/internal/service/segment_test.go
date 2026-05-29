package service

import "testing"

func TestResourceSegmentReviewedStatus(t *testing.T) {
	if SegmentStatusReviewed != "reviewed" {
		t.Fatalf("SegmentStatusReviewed = %q, want reviewed", SegmentStatusReviewed)
	}
}
