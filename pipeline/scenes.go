package pipeline

import (
	"time"

	"github.com/hekmon/sptenc/core"
	"github.com/hekmon/sptenc/ffmpeg"
)

// FilterShortScenes wraps core.FilterShortScenes, handling the type conversion
// between ffmpeg.Scene and core.Scene.
func FilterShortScenes(scenes []ffmpeg.Scene, frameRate string, totalDuration, minDuration time.Duration) ([]ffmpeg.Scene, error) {
	if minDuration <= 0 || len(scenes) == 0 {
		return scenes, nil
	}
	filtered, err := core.FilterShortScenes(ToCoreScenes(scenes), frameRate, totalDuration, minDuration)
	if err != nil {
		return nil, err
	}
	return FromCoreScenes(filtered), nil
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
