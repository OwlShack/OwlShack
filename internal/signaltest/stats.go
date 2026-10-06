package signaltest

import (
	"encoding/json"
	"math"

	"github.com/OwlShack/OwlShack/internal/store"
)

// ValueStats summarizes a set of samples across a test's runs.
type ValueStats struct {
	N      int     `json:"n"`
	Min    float64 `json:"min"`
	Max    float64 `json:"max"`
	Mean   float64 `json:"mean"`
	StdDev float64 `json:"stddev"`
}

// HopStats is ValueStats for one 1-indexed hop position.
type HopStats struct {
	Hop int `json:"hop"`
	ValueStats
}

// Stats is computed on read from the stored runs, never persisted.
type Stats struct {
	Total       int         `json:"total"`
	OKCount     int         `json:"okCount"`
	Timeouts    int         `json:"timeouts"`
	SuccessRate float64     `json:"successRate"`
	PerHop      []HopStats  `json:"perHop"`
	FinalSNR    *ValueStats `json:"finalSnr,omitempty"`
	ElapsedMs   *ValueStats `json:"elapsedMs,omitempty"`
}

// ComputeStats aggregates a test's runs; timed-out runs still contribute the hops they heard to PerHop, showing where a link drops.
func ComputeStats(runs []store.SignalTestRun) Stats {
	var s Stats
	s.Total = len(runs)

	hopValues := map[int][]float64{}
	var finalSNRs, elapsedMs []float64

	for _, run := range runs {
		if run.OK {
			s.OKCount++
		}
		var hops []float64
		if err := json.Unmarshal([]byte(run.HopSNRs), &hops); err == nil {
			for i, v := range hops {
				hopValues[i] = append(hopValues[i], v)
			}
		}
		if run.OK {
			if run.SNR != nil {
				finalSNRs = append(finalSNRs, *run.SNR)
			}
			elapsedMs = append(elapsedMs, float64(run.ElapsedMs))
		}
	}
	s.Timeouts = s.Total - s.OKCount
	if s.Total > 0 {
		s.SuccessRate = float64(s.OKCount) / float64(s.Total)
	}

	maxHop := 0
	for i := range hopValues {
		if i+1 > maxHop {
			maxHop = i + 1
		}
	}
	for i := 0; i < maxHop; i++ {
		vs := computeValueStats(hopValues[i])
		if vs == nil {
			continue
		}
		s.PerHop = append(s.PerHop, HopStats{Hop: i + 1, ValueStats: *vs})
	}

	s.FinalSNR = computeValueStats(finalSNRs)
	s.ElapsedMs = computeValueStats(elapsedMs)
	return s
}

func computeValueStats(values []float64) *ValueStats {
	if len(values) == 0 {
		return nil
	}
	vs := &ValueStats{N: len(values), Min: values[0], Max: values[0]}
	sum := 0.0
	for _, v := range values {
		if v < vs.Min {
			vs.Min = v
		}
		if v > vs.Max {
			vs.Max = v
		}
		sum += v
	}
	vs.Mean = sum / float64(len(values))

	if len(values) > 1 {
		var sq float64
		for _, v := range values {
			d := v - vs.Mean
			sq += d * d
		}
		vs.StdDev = math.Sqrt(sq / float64(len(values)-1)) // sample stddev
	}
	return vs
}
