package pipeline

import (
	"math"
	"time"

	"github.com/hekmon/sptenc/core"
	"github.com/hekmon/sptenc/ffmpeg"
)

// FilterShortScenes wraps core.FilterShortScenes, handling the type conversion
// between ffmpeg.Scene and core.Scene.
func FilterShortScenes(scenes []ffmpeg.Scene, totalDuration, minDuration time.Duration) []ffmpeg.Scene {
	if minDuration <= 0 || len(scenes) == 0 {
		return scenes
	}
	return FromCoreScenes(core.FilterShortScenes(ToCoreScenes(scenes), totalDuration, minDuration))
}

// ToCoreScenes converts ffmpeg scenes to core scenes.
func ToCoreScenes(scenes []ffmpeg.Scene) []core.Scene {
	coreScenes := make([]core.Scene, len(scenes))
	for i, s := range scenes {
		coreScenes[i] = core.Scene{Frame: s.Frame, Start: s.Start, Score: s.Score}
	}
	return coreScenes
}

// FromCoreScenes converts core scenes to ffmpeg scenes.
func FromCoreScenes(coreScenes []core.Scene) []ffmpeg.Scene {
	scenes := make([]ffmpeg.Scene, len(coreScenes))
	for i, s := range coreScenes {
		scenes[i] = ffmpeg.Scene{Frame: s.Frame, Start: s.Start, Score: s.Score}
	}
	return scenes
}

// ReusableThreshold converts the score of a scene into a threshold that, handed back to scene
// detection (encode -T), keeps that scene and gives the very same scenes as here.
//
// # WHY THE SCORE ITSELF CAN NOT BE USED
//
// A score is known as printed by ffmpeg: rounded to SceneScoreResolution. But ffmpeg compares
// the threshold with the real score. A scene printed as 24.277 can really score 24.2768:
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
func ReusableThreshold(score, minThreshold float64) float64 {
	const precision = 10 / ffmpeg.SceneScoreResolution // one more decimal than the scores
	threshold := math.Round((score-ffmpeg.SceneScoreResolution/2)*precision) / precision
	return math.Max(threshold, minThreshold)
}
