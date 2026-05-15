package dynamicbatch

import "testing"

func TestComputeFlushThreshold_BacklogZero_ReturnsSizeMax(t *testing.T) {
	sizeMin := 250
	sizeInitial := 1000
	sizeMax := 2000

	got := ComputeFlushThreshold(sizeMin, sizeInitial, sizeMax, 0, 10000)
	if got != sizeMax {
		t.Fatalf("expected %d, got %d", sizeMax, got)
	}
}

func TestComputeFlushThreshold_BacklogHalf_ReturnsSizeInitial(t *testing.T) {
	sizeMin := 250
	sizeInitial := 1000
	sizeMax := 2000
	chCap := 1000

	// backlog == chCap/2 => r==0.5 => threshold == sizeInitial
	got := ComputeFlushThreshold(sizeMin, sizeInitial, sizeMax, chCap/2, chCap)
	if got != sizeInitial {
		t.Fatalf("expected %d, got %d", sizeInitial, got)
	}
}

func TestComputeFlushThreshold_BacklogFull_ReturnsSizeMin(t *testing.T) {
	sizeMin := 250
	sizeInitial := 1000
	sizeMax := 2000
	chCap := 1000

	got := ComputeFlushThreshold(sizeMin, sizeInitial, sizeMax, chCap, chCap)
	if got != sizeMin {
		t.Fatalf("expected %d, got %d", sizeMin, got)
	}
}

func TestComputeFlushThreshold_MonotonicDecreasing(t *testing.T) {
	sizeMin := 250
	sizeInitial := 1000
	sizeMax := 2000
	chCap := 1000

	prev := ComputeFlushThreshold(sizeMin, sizeInitial, sizeMax, 0, chCap)
	for backlog := 1; backlog <= chCap; backlog += 37 {
		got := ComputeFlushThreshold(sizeMin, sizeInitial, sizeMax, backlog, chCap)
		if got > prev {
			t.Fatalf("threshold should be non-increasing: backlog=%d got=%d prev=%d", backlog, got, prev)
		}
		prev = got
	}
}

func TestComputeFlushThreshold_ClampsToRange(t *testing.T) {
	sizeMin := 250
	sizeInitial := 1000
	sizeMax := 2000

	lo := sizeMin
	hi := sizeMax

	for _, tc := range []struct {
		name    string
		sMin    int
		sInit   int
		sMax    int
		backlog int
		chCap   int
	}{
		{"negSizeMin", -10, sizeInitial, sizeMax, 0, 1000},
		{"negSizeInitial", sizeMin, -1, sizeMax, 0, 1000},
		{"negSizeMax", sizeMin, sizeInitial, -1, 0, 1000},
		{"weirdOrder", sizeMax, sizeMin, sizeInitial, 0, 1000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := ComputeFlushThreshold(tc.sMin, tc.sInit, tc.sMax, tc.backlog, tc.chCap)
			if got < lo || got > hi {
				t.Fatalf("expected threshold in [%d,%d], got %d (tc=%+v)", lo, hi, got, tc)
			}
		})
	}
}
