package transnetv2

import (
	"fmt"
	"strconv"
)

const (
	// ProbabilityFloor is the probability a frame must reach to belong to a run, the runs giving
	// the cuts: see WHY THE FLOOR.
	ProbabilityFloor = 0.3
	// ScoreDecimals is the number of decimals a cut's score is rounded to: see WHY THE SCORE IS
	// ROUNDED.
	ScoreDecimals = 4
)

// Cut is a cut found by the detector.
type Cut struct {
	// Frame is the index of the first frame of the new scene, counted from 0: the frame after the
	// peak of its run, see WHY THE FRAME AFTER THE PEAK.
	Frame int
	// Score is the probability of the peak, rounded to ScoreDecimals decimals.
	Score float64
}

// Cuts turns the probabilities of Detect into cuts, ordered by frame. A run is a maximal sequence
// of consecutive frames whose probability is at or above ProbabilityFloor, each 32-bit probability
// compared exactly with the floor (float32's 0.3 is 0.300000011920929 and reaches it, the float32
// under it, 0.299999982118607, does not). Each run gives one cut, at its peak, the first of its
// equal maxima, except a run peaking on the last frame: no frame comes after it to start a scene.
// It fails on a probability that is NaN or out of 0 to 1, which Detect never returns.
//
// This is the detection of the seedvr2x project (seedvr2x/core/seedvr2x/shotcuts.py, detected at
// a threshold of 0.3, whose _exact widens each float32 to the double of its exact value before
// comparing it with the threshold, and whose _runs takes np.argmax, the first maximum).
//
// # WHY THE FLOOR
//
// The floor is the lowest threshold sptenc offers: 0.3, seedvr2x's default (its THRESHOLD), under
// which its measurement, as its comments report it, found each cut gained to cost 6 to 14 false
// cuts on animation, 14 to 21 on live action, and a false cut costs more in an encoder than a
// missed one.
//
// The runs are cut at the floor, so a lower floor merges neighbouring cuts at every threshold
// above it: on [0, 0.9, 0.2, 0.8, 0], a floor of 0.1 makes one run, hence one cut (frame 2), which
// stays alone at a threshold of 0.3, where a floor of 0.3 makes two runs and two cuts (frames 2
// and 4), seedvr2x's detection at 0.3. With the floor at 0.3, the cuts are exactly that detection,
// and a higher threshold can only drop cuts by their own score.
//
// The limit that remains: two cuts inside one run at 0.3 (the probability not falling under 0.3
// between them) are one cut at every threshold, as in seedvr2x's detection at any threshold.
//
// # WHY THE FRAME AFTER THE PEAK
//
// TransNetV2 gives each frame its probability of being the last frame of a shot: the peak is the
// outgoing shot's last frame (seedvr2x measured an offset of -1 on 98% of its sure cuts), and the
// new scene starts on the next one.
//
// # WHY THE SCORE IS ROUNDED
//
// A float32 widened to a double prints with digits of its binary representation, not of the
// model (0.300000011920929 for float32's 0.3): the score is meant to be printed, recorded and read
// back as a threshold, as scdet's scores are with their 3 decimals. 4 decimals keep apart scores a
// ten-thousandth apart. The rounding is the one of the decimal printed (strconv, round half to
// even on an exact tie, as Python's round), so the score is exactly what is shown.
//
// The rounding is sptenc's own: seedvr2x keeps the exact float32. Compared with a threshold, a
// rounded score can differ from the exact comparison: float32's 0.7, 0.699999988, scores 0.7 and
// reaches a threshold of 0.7 when compared rounded, where it does not exactly. The floor itself is
// compared with the exact value.
func Cuts(probabilities []float32) (cuts []Cut, err error) {
	for i, p := range probabilities {
		if !(p >= 0 && p <= 1) {
			return nil, fmt.Errorf("probability %v of frame %d is not between 0 and 1", p, i)
		}
	}
	for first := 0; first < len(probabilities); first++ {
		if float64(probabilities[first]) < ProbabilityFloor {
			continue
		}
		peak, end := first, first+1
		for ; end < len(probabilities) && float64(probabilities[end]) >= ProbabilityFloor; end++ {
			if probabilities[end] > probabilities[peak] { // strictly: the first of equal maxima
				peak = end
			}
		}
		if peak+1 < len(probabilities) {
			cuts = append(cuts, Cut{Frame: peak + 1, Score: roundScore(probabilities[peak])})
		}
		first = end // the frame at end, if any, is under the floor
	}
	return cuts, nil
}

// roundScore rounds p to ScoreDecimals decimals, as it is printed: see WHY THE SCORE IS ROUNDED.
func roundScore(p float32) float64 {
	score, _ := strconv.ParseFloat(strconv.FormatFloat(float64(p), 'f', ScoreDecimals, 64), 64)
	return score
}
