package gateway

import (
	"math"
	"sort"
	"sync/atomic"
	"time"
)

const transportHistogramBucketCount = 19

var transportHistogramUpperBoundsMicroseconds = [transportHistogramBucketCount]uint64{
	1, 5, 10, 25, 50, 100, 250, 500, 1000, 2500, 5000, 10000, 25000,
	50000, 100000, 250000, 500000, 1000000, math.MaxUint64,
}

type TransportHistogramSnapshot struct {
	Count               uint64
	SumMicroseconds     uint64
	MaximumMicroseconds uint64
	BucketCounts        [transportHistogramBucketCount]uint64
}

func (snapshot TransportHistogramSnapshot) PercentileMicroseconds(percentile uint32) uint64 {
	if snapshot.Count == 0 || percentile == 0 || percentile > 100 {
		return 0
	}
	// Split the calculation so a long-lived process cannot overflow Count * percentile.
	quotient := snapshot.Count / 100
	remainder := snapshot.Count % 100
	target := quotient*uint64(percentile) +
		(remainder*uint64(percentile)+99)/100
	var cumulative uint64
	for index, count := range snapshot.BucketCounts {
		cumulative += count
		if cumulative >= target {
			return transportHistogramUpperBoundsMicroseconds[index]
		}
	}
	return math.MaxUint64
}

type TransportMetricsSnapshot struct {
	Deserialization TransportHistogramSnapshot
}

type TransportMetrics struct {
	deserializationCount   atomic.Uint64
	deserializationSum     atomic.Uint64
	deserializationMaximum atomic.Uint64
	deserializationBuckets [transportHistogramBucketCount]atomic.Uint64
}

func (metrics *TransportMetrics) observeDeserialization(duration time.Duration) {
	microseconds := duration.Microseconds()
	if microseconds < 0 {
		microseconds = 0
	}
	value := uint64(microseconds)
	metrics.deserializationCount.Add(1)
	metrics.deserializationSum.Add(value)
	current := metrics.deserializationMaximum.Load()
	for current < value && !metrics.deserializationMaximum.CompareAndSwap(current, value) {
		current = metrics.deserializationMaximum.Load()
	}
	index := sort.Search(len(transportHistogramUpperBoundsMicroseconds), func(index int) bool {
		return transportHistogramUpperBoundsMicroseconds[index] >= value
	})
	metrics.deserializationBuckets[index].Add(1)
}

func (metrics *TransportMetrics) snapshot() TransportMetricsSnapshot {
	result := TransportMetricsSnapshot{}
	result.Deserialization.Count = metrics.deserializationCount.Load()
	result.Deserialization.SumMicroseconds = metrics.deserializationSum.Load()
	result.Deserialization.MaximumMicroseconds = metrics.deserializationMaximum.Load()
	for index := range metrics.deserializationBuckets {
		result.Deserialization.BucketCounts[index] = metrics.deserializationBuckets[index].Load()
	}
	return result
}
