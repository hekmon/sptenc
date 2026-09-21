package pipeline

import (
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
	coreScenes := make([]core.Scene, len(scenes))
	for i, s := range scenes {
		coreScenes[i] = core.Scene{Start: s.Start, Score: s.Score}
	}
	filtered := core.FilterShortScenes(coreScenes, totalDuration, minDuration)
	result := make([]ffmpeg.Scene, len(filtered))
	for i, s := range filtered {
		result[i] = ffmpeg.Scene{Start: s.Start, Score: s.Score}
	}
	return result
}
