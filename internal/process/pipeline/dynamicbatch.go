package pipeline

import "math"

// ComputeFlushThreshold computes a dynamic flush threshold (i.e. an
// effective "batch size") based on current backlog.
//
// sizeMin: lower bound for the effective batch size
// sizeInitial: effective batch size when backlog is around "mid"
// sizeMax: upper bound for the effective batch size
// backlog: current queued line count
// chCap: channel capacity used to normalize backlog into [0,1]
//
// Backlog ratio r = backlog/chCap in [0,1]:
// - r == 0.0  => threshold == sizeMax
// - r == 0.5  => threshold == sizeInitial
// - r == 1.0  => threshold == sizeMin
//
// Implementation uses a piecewise-linear interpolation so that
// (sizeMax, sizeInitial, sizeMin) are hit exactly at r=0, r=0.5, r=1.
// Function is deterministic and side-effect free.
func ComputeFlushThreshold(sizeMin, sizeInitial, sizeMax int, backlog int, chCap int) int {
	// Sanitize and derive sane ordering.
	if sizeMin <= 0 {
		sizeMin = 1
	}
	if sizeInitial <= 0 {
		sizeInitial = sizeMin
	}
	if sizeMax <= 0 {
		sizeMax = sizeInitial
	}

	// Ensure sizeMin <= sizeInitial <= sizeMax (clamp).
	if sizeMin > sizeInitial {
		sizeInitial = sizeMin
	}
	if sizeInitial > sizeMax {
		sizeMax = sizeInitial
	}
	if sizeMax < sizeMin {
		sizeMin = sizeMax
	}

	if chCap <= 0 {
		// No meaningful ratio; choose the "backlog low" side.
		return sizeMax
	}
	if backlog <= 0 {
		return sizeMax
	}

	r := float64(backlog) / float64(chCap)
	if r <= 0 {
		return sizeMax
	}
	if r >= 1 {
		return sizeMin
	}

	half := 0.5
	if r <= half {
		// Interpolate [sizeMax .. sizeInitial] for r in [0 .. 0.5]
		t := r / half // [0..1]
		v := float64(sizeMax) + float64(sizeInitial-sizeMax)*t
		out := int(math.Round(v))
		return clampInt(out, sizeMin, sizeMax)
	}

	// Interpolate [sizeInitial .. sizeMin] for r in [0.5 .. 1]
	t := (r - half) / half // [0..1]
	v := float64(sizeInitial) + float64(sizeMin-sizeInitial)*t
	out := int(math.Round(v))
	return clampInt(out, sizeMin, sizeMax)
}

func clampInt(x, lo, hi int) int {
	if x < lo {
		return lo
	}
	if x > hi {
		return hi
	}
	return x
}
