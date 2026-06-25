package handlers

import "time"

// bucketTimestamps distributes events into N equal-width time buckets between
// start and end and returns the per-bucket count as a slice of length buckets.
// Events exactly on the end boundary are counted in the last bucket.
func bucketTimestamps(events []time.Time, start, end time.Time, buckets int) []int {
	result := make([]int, buckets)
	if buckets <= 0 || end.Equal(start) || end.Before(start) {
		return result
	}
	window := end.Sub(start)
	for _, t := range events {
		if t.Before(start) || t.After(end) {
			continue
		}
		idx := int(t.Sub(start) * time.Duration(buckets) / window)
		if idx >= buckets {
			idx = buckets - 1
		}
		result[idx]++
	}
	return result
}
