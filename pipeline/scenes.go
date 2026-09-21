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
	return FromCoreScenes(core.FilterShortScenes(ToCoreScenes(scenes), totalDuration, minDuration))
}

// ToCoreScenes converts ffmpeg scenes to core scenes.
func ToCoreScenes(scenes []ffmpeg.Scene) []core.Scene {
	coreScenes := make([]core.Scene, len(scenes))
	for i, s := range scenes {
		coreScenes[i] = core.Scene{Start: s.Start, Score: s.Score}
	}
	return coreScenes
}

// FromCoreScenes converts core scenes to ffmpeg scenes.
func FromCoreScenes(coreScenes []core.Scene) []ffmpeg.Scene {
	scenes := make([]ffmpeg.Scene, len(coreScenes))
	for i, s := range coreScenes {
		scenes[i] = ffmpeg.Scene{Start: s.Start, Score: s.Score}
	}
	return scenes
}
