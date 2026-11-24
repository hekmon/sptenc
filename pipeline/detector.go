package pipeline

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/hekmon/sptenc/ffmpeg"
	"github.com/hekmon/sptenc/transnetv2"
)

// SceneDetector names a scene detector: what finds the scene boundaries the video is cut at,
// and scores them. Each detector scores on its own scale, so everything that reads or writes a
// threshold (its range, its defaults, how a score becomes a reusable threshold, how both are
// printed) asks the detector. The detection itself is dispatched by the CLI (liveDetectScenes).
//
// Every property comes from one explicit switch over the detectors (detectorPropertiesOf), as the
// encoders are mapped in EncoderAdapter: adding a detector is adding one complete case there, and
// a listed detector with an incomplete one fails the tests of this file.
//
// A SceneDetector comes from one of the constants below or from ParseSceneDetector. Any other
// value (the zero value of a field left unset included) is unknown: it never crashes a run, which
// can have been going for days. ValidateThreshold refuses every threshold for it, the detection
// itself refuses it (liveDetectScenes), and the format methods print a plain number.
type SceneDetector string

const (
	// SceneDetectorScdet is ffmpeg's scdet filter (see ffmpeg.ScenesDetection), the default.
	SceneDetectorScdet SceneDetector = "scdet"
	// SceneDetectorTransNetV2 is TransNetV2, a neural network finding shot boundaries, run by its
	// helper executable (see the transnetv2 package and DetectScenesTransNetV2).
	SceneDetectorTransNetV2 SceneDetector = "transnetv2"
)

// SceneDetectors lists the detectors, the default first: for flag validation and help.
var SceneDetectors = []SceneDetector{SceneDetectorScdet, SceneDetectorTransNetV2}

// SceneDetectorNames returns the names of the detectors, the default first.
func SceneDetectorNames() []string {
	names := make([]string, len(SceneDetectors))
	for i, detector := range SceneDetectors {
		names[i] = string(detector)
	}
	return names
}

// ParseSceneDetector returns the detector of that name.
func ParseSceneDetector(name string) (SceneDetector, error) {
	for _, detector := range SceneDetectors {
		if string(detector) == name {
			return detector, nil
		}
	}
	return "", fmt.Errorf("invalid scene detector %q, valid values are: %s", name, strings.Join(SceneDetectorNames(), ", "))
}

// String returns the name of the detector.
func (d SceneDetector) String() string {
	return string(d)
}

// detectorProperties holds every property of a detector, on its own scale. The zero value is the
// one of an unknown detector: known is false, and every method checks it before trusting the rest.
type detectorProperties struct {
	known                            bool
	thresholdMin, thresholdMax       float64 // range of a threshold, bounds included
	defaultThreshold                 float64 // single detection: encode, split
	defaultSearchMin                 float64 // search floor: batchsearch, thresholds
	defaultSearchMax                 float64 // search ceiling: batchsearch, thresholds
	scoreResolution                  float64 // step of the scores as the detector reports them, see ReusableThreshold
	scoreDecimals, thresholdDecimals int     // decimals printed, -1 for every decimal the value has
	// thresholdOnReportedScore tells that the threshold is compared with the score as reported,
	// rounded, and not with an unrounded one the detector keeps to itself: see ReusableThreshold
	thresholdOnReportedScore bool
	// helper tells that the detector runs in a helper executable of its own, which reads frames of
	// frameWidth x frameHeight decoded and scaled by sptenc (see HelperFrameSize)
	helper                  bool
	frameWidth, frameHeight int
}

// detectorPropertiesOf returns the properties of the detector, the zero value for an unknown one.
// The only switch on the detector: each case is complete.
func detectorPropertiesOf(d SceneDetector) detectorProperties {
	switch d {
	case SceneDetectorScdet:
		return detectorProperties{
			known: true,
			// a percentage of the pixel range, see the scdet defaults below;
			// ffmpeg.ScenesDetection checks it again on its own
			thresholdMin:     ffmpeg.SceneThresholdMin,
			thresholdMax:     ffmpeg.SceneThresholdMax,
			defaultThreshold: scdetThresholdDefault,
			defaultSearchMin: scdetMinThresholdDefault,
			defaultSearchMax: scdetMaxThresholdDefault,
			// scores read from ffmpeg's log lines, rounded to 3 decimals there
			scoreResolution: ffmpeg.SceneScoreResolution,
			// a scene list only has to tell the boundaries apart, a threshold is pasted back:
			// every decimal it has (a reusable threshold has one more than the scores)
			scoreDecimals:     2,
			thresholdDecimals: -1,
		}
	case SceneDetectorTransNetV2:
		return detectorProperties{
			known: true,
			// a probability; nothing exists under the floor, where the runs of frames giving the
			// cuts are cut (see transnetv2.Cuts, WHY THE FLOOR)
			thresholdMin:     transnetv2.ProbabilityFloor,
			thresholdMax:     1,
			defaultThreshold: transnetv2ThresholdDefault,
			defaultSearchMin: transnetv2MinThresholdDefault,
			defaultSearchMax: transnetv2MaxThresholdDefault,
			// scores rounded by sptenc itself (transnetv2.Cuts, WHY THE SCORE IS ROUNDED), and the
			// threshold compared with that very rounded score (DetectScenesTransNetV2)
			scoreResolution:          math.Pow10(-transnetv2.ScoreDecimals),
			thresholdOnReportedScore: true,
			// every decimal a score has, which a threshold pasted back has too
			scoreDecimals:     transnetv2.ScoreDecimals,
			thresholdDecimals: -1,
			// the frames the helper reads (see the protocol in the transnetv2 package)
			helper:      true,
			frameWidth:  transnetv2.FrameWidth,
			frameHeight: transnetv2.FrameHeight,
		}
	default:
		return detectorProperties{}
	}
}

// ThresholdRange returns the range a threshold of the detector must be in, bounds included (0 to 0
// for an unknown detector, which ValidateThreshold refuses anyway).
func (d SceneDetector) ThresholdRange() (min, max float64) {
	p := detectorPropertiesOf(d)
	return p.thresholdMin, p.thresholdMax
}

// ValidateThreshold returns an error when the threshold is out of the range of the detector, and
// for any threshold when the detector is unknown.
func (d SceneDetector) ValidateThreshold(threshold float64) error {
	p := detectorPropertiesOf(d)
	if !p.known {
		return fmt.Errorf("unknown scene detector %q", string(d))
	}
	if threshold < p.thresholdMin || threshold > p.thresholdMax {
		return fmt.Errorf("must be between %s and %s with the %s detector",
			d.FormatThreshold(p.thresholdMin), d.FormatThreshold(p.thresholdMax), d)
	}
	return nil
}

// Scdet defaults.
//
// The scdet score is the mean absolute difference between two consecutive frames, as a
// percentage of the pixel range, or its change from the previous frame if smaller (so a
// sustained pan scores low, a cut scores high). ffmpeg documents "good values" in [8, 14]
// with 10 as the filter default, without any justification: no published study measures
// scdet against a ground truth, and none could answer the question asked here anyway, which
// is not "is this a cut" but "does splitting here give a smaller file".
//
// The single-threshold default (encode, split) is ffmpeg's own 10, the middle of that range:
// a blind guess should not sit at an edge of it.
//
// The search floor is the bottom of that range. It used to be 14 because a low threshold
// turned every pan and flash into segments of a few frames, and the floor was doubling as
// the quality guardrail. Since the minimum segment length (--min-segment-length) took that
// role, a low threshold only adds cuts that survive as segments of 5 s or more, which is
// exactly what makes the file smaller: the search only walks upward and stops on strikes,
// and with the floor at 14 too many searches were won by their first candidate, which means
// a clipped range, not an optimum. Not lower than 8: below the documented range scdet flags
// motion onsets, every unique score is a raw candidate (each one costing a merge pass on the
// full markers list) and the candidates budget gets spread thinner.
//
// The ceiling has no rationale at all (it came in with the 14, before the segment filter,
// and was 40 before that): the scores of real hard cuts stay far below it, and the search
// stops long before on strikes. Its only real effect is on the candidates budget, which the
// auto-tuned scene drop spreads over the whole range: a lower ceiling samples the low end,
// where the winners are, more finely. To be revisited with a body of real search results.
const (
	scdetThresholdDefault    = 10.0 // single-threshold commands: encode, split
	scdetMinThresholdDefault = 8.0  // search floor: batchsearch, thresholds
	scdetMaxThresholdDefault = 40.0 // search ceiling: batchsearch, thresholds
)

// TransNetV2 defaults. PROVISIONAL: they are not measured for sptenc, they wait for a comparison
// of the file sizes real encodes give, as scdet's wait for a body of real search results.
//
// The score is the probability the model gives the last frame of the outgoing shot (see
// transnetv2.Cuts). Their basis is the study of the seedvr2x project, as the comments of
// transnetv2/cuts.go and its decision brief (seedvr2x/research/docs/scene-detection.md) report it:
// two rounds of labelled candidates (130 rows, 84 animated, 46 live action) over 11 sources. That
// study asked "is this a cut" for an upscaler, where a false cut costs nothing; here the question
// is whether splitting gives a smaller file, which no study has measured for either detector.
//
// The single-threshold default (encode, split) is 0.5, the study's working threshold: recall 0.80
// and precision 0.88 on animation, 0.95 and 0.86 on live action (scdet at 10: 0.84 and 0.69, 0.24
// and 0.86), with hardly any burst (5 per hour of animation against scdet's 351). seedvr2x itself
// detects at 0.3, for 7 more real cuts and 3 more false ones on the labelled round; a false cut
// costs more in an encoder than a missed one (transnetv2.Cuts, WHY THE FLOOR), so a single guess
// stays at 0.5.
//
// The search floor is the floor of the detector, 0.3 (transnetv2.ProbabilityFloor): a threshold
// under it can not exist, and the search walks upward from it, so that the cuts 0.5 would miss
// (7 of the 11 the study's 0.5 missed score 0.35 to 0.49) are candidates.
//
// The ceiling, 0.9, is the highest threshold the study counted (617 detections per hour of
// animation, 431 of live action, no burst): beyond it nothing is measured. As with scdet, its main
// effect is on the candidates budget, spread over the whole range.
const (
	transnetv2ThresholdDefault    = 0.5                         // single-threshold commands: encode, split
	transnetv2MinThresholdDefault = transnetv2.ProbabilityFloor // search floor: batchsearch, thresholds
	transnetv2MaxThresholdDefault = 0.9                         // search ceiling: batchsearch, thresholds
)

// DefaultThreshold returns the threshold of a single detection (encode, split) when none is given.
func (d SceneDetector) DefaultThreshold() float64 {
	return detectorPropertiesOf(d).defaultThreshold
}

// DefaultSearchRange returns the range of a threshold search (batchsearch, thresholds) when its
// bounds are not given: the threshold the scenes are detected with, and the highest candidate.
func (d SceneDetector) DefaultSearchRange() (min, max float64) {
	p := detectorPropertiesOf(d)
	return p.defaultSearchMin, p.defaultSearchMax
}

// ReusableThreshold converts the score of a scene into a threshold that, handed back to scene
// detection (encode -T) with the same detector, keeps that scene and gives the very same scenes
// as here. minThreshold is the threshold the scenes were detected with. An unknown detector has no
// resolution to build on: the score is returned as is (never below minThreshold).
//
// What follows is scdet's case, where the detector compares the threshold with a score it does
// not report. A detector comparing the threshold with the very score it reports
// (thresholdOnReportedScore: transnetv2, whose scores sptenc rounds itself and compares rounded,
// see DetectScenesTransNetV2) has nothing to subtract: the score itself keeps that scene, and
// any scene scoring less is reported less. Its reusable threshold is the score, never below
// minThreshold.
//
// # WHY THE SCORE ITSELF CAN NOT BE USED
//
// A score is known as printed by ffmpeg: rounded to the resolution of the detector
// (ffmpeg.SceneScoreResolution for scdet). But ffmpeg compares the threshold with the real
// score. A scene printed as 24.277 can really score 24.2768:
// detecting again with a threshold of 24.277 silently loses it, and the threshold reported by a
// search does not reproduce its own scenes (about one time out of two, depending on which side
// the rounding went).
//
// # WHY HALF A RESOLUTION STEP BELOW
//
// A printed score p means a real score within [p-step/2, p+step/2]. The threshold p-step/2 is
// at or under the real score of that scene whatever the rounding, and still above any scene
// printed with a lower score (at most p-step, so really under p-step/2). Any lower value
// could let such a scene in; any higher value could lose the scene itself.
//
// # EDGE CASES
//
//   - The result is never lower than minThreshold, the threshold the scenes were detected with:
//     going below would let scenes in that were never seen here. At minThreshold exactly, a new
//     detection is the same detection.
//   - The result is rounded to one decimal more than the scores to get rid of the floating
//     point noise of the subtraction (24.2765, not 24.276500000000002): it is shown to the user.
func (d SceneDetector) ReusableThreshold(score, minThreshold float64) float64 {
	p := detectorPropertiesOf(d)
	if !p.known || p.scoreResolution <= 0 {
		return math.Max(score, minThreshold)
	}
	if p.thresholdOnReportedScore {
		return math.Max(score, minThreshold)
	}
	precision := 10 / p.scoreResolution // one more decimal than the scores
	threshold := math.Round((score-p.scoreResolution/2)*precision) / precision
	return math.Max(threshold, minThreshold)
}

// HasHelper tells whether the detector runs in a helper executable of its own, which reads frames
// decoded and scaled by sptenc (see HelperFrameSize): scdet does not, it is a filter of ffmpeg. A
// detector with a helper is checked at start-up, and the helper's version is recorded in the tags
// of an encode (see metadata.SceneCuts).
func (d SceneDetector) HasHelper() bool {
	p := detectorPropertiesOf(d)
	return p.known && p.helper
}

// HelperFrameSize returns the size of the frames the helper of the detector reads, 0 by 0 for a
// detector without a helper.
func (d SceneDetector) HelperFrameSize() (width, height int) {
	p := detectorPropertiesOf(d)
	return p.frameWidth, p.frameHeight
}

// FormatThreshold formats a threshold of the detector for display, and for the user to paste
// back. An unknown detector gets every decimal the value has.
func (d SceneDetector) FormatThreshold(threshold float64) string {
	p := detectorPropertiesOf(d)
	if !p.known {
		return strconv.FormatFloat(threshold, 'f', -1, 64)
	}
	return strconv.FormatFloat(threshold, 'f', p.thresholdDecimals, 64)
}

// FormatScore formats a score of the detector for display in a scene list, where it only has to
// tell the boundaries apart, not be pasted back (FormatThreshold is for that). An unknown detector
// gets every decimal the value has.
func (d SceneDetector) FormatScore(score float64) string {
	p := detectorPropertiesOf(d)
	if !p.known {
		return strconv.FormatFloat(score, 'f', -1, 64)
	}
	return strconv.FormatFloat(score, 'f', p.scoreDecimals, 64)
}
