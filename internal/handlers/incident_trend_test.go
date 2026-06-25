package handlers

import (
	"testing"
	"time"
)

func TestBucketTimestamps_Empty(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)
	got := bucketTimestamps(nil, start, end, 12)
	if len(got) != 12 {
		t.Fatalf("len = %d, want 12", len(got))
	}
	for i, v := range got {
		if v != 0 {
			t.Errorf("bucket[%d] = %d, want 0", i, v)
		}
	}
}

func TestBucketTimestamps_AllInOneBucket(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(12 * time.Minute)
	// 3 events in the first minute → bucket 0
	events := []time.Time{
		start.Add(10 * time.Second),
		start.Add(30 * time.Second),
		start.Add(50 * time.Second),
	}
	got := bucketTimestamps(events, start, end, 12)
	if got[0] != 3 {
		t.Errorf("bucket[0] = %d, want 3", got[0])
	}
	for i := 1; i < 12; i++ {
		if got[i] != 0 {
			t.Errorf("bucket[%d] = %d, want 0", i, got[i])
		}
	}
}

func TestBucketTimestamps_SpanningAllBuckets(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(12 * time.Minute)
	// One event per minute, landing in each of the 12 buckets.
	events := make([]time.Time, 12)
	for i := range events {
		events[i] = start.Add(time.Duration(i)*time.Minute + 30*time.Second)
	}
	got := bucketTimestamps(events, start, end, 12)
	total := 0
	for _, v := range got {
		total += v
	}
	if total != 12 {
		t.Errorf("total events = %d, want 12", total)
	}
	for i, v := range got {
		if v != 1 {
			t.Errorf("bucket[%d] = %d, want 1", i, v)
		}
	}
}

func TestBucketTimestamps_BoundaryEdgeCases(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(12 * time.Minute)

	// Event exactly at start → bucket 0.
	got := bucketTimestamps([]time.Time{start}, start, end, 12)
	if got[0] != 1 {
		t.Errorf("event at start: bucket[0] = %d, want 1", got[0])
	}

	// Event exactly at end → last bucket.
	got = bucketTimestamps([]time.Time{end}, start, end, 12)
	if got[11] != 1 {
		t.Errorf("event at end: bucket[11] = %d, want 1", got[11])
	}

	// Event before start → discarded.
	got = bucketTimestamps([]time.Time{start.Add(-time.Second)}, start, end, 12)
	for i, v := range got {
		if v != 0 {
			t.Errorf("before-start event: bucket[%d] = %d, want 0", i, v)
		}
	}

	// Event after end → discarded.
	got = bucketTimestamps([]time.Time{end.Add(time.Second)}, start, end, 12)
	for i, v := range got {
		if v != 0 {
			t.Errorf("after-end event: bucket[%d] = %d, want 0", i, v)
		}
	}
}

func TestBucketTimestamps_ZeroBuckets(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)
	got := bucketTimestamps([]time.Time{start.Add(time.Minute)}, start, end, 0)
	if len(got) != 0 {
		t.Errorf("len = %d, want 0", len(got))
	}
}

func TestBucketTimestamps_EqualStartEnd(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	got := bucketTimestamps([]time.Time{t0}, t0, t0, 12)
	for i, v := range got {
		if v != 0 {
			t.Errorf("equal start/end: bucket[%d] = %d, want 0", i, v)
		}
	}
}
