package core

import (
	"fmt"
	"os"

	"github.com/hekmon/sptenc/ffmpeg"
)

func getFileSize(path string) (size int64, err error) {
	info, err := os.Stat(path)
	if err != nil {
		err = fmt.Errorf("failed to stat path: %w", err)
	} else {
		size = info.Size()
	}
	return
}

// AllAudioTracksPCM returns true if all audio streams in the given stats are PCM encoded.
func AllAudioTracksPCM(stats ffmpeg.FFProbeStats) bool {
	for _, stream := range stats.Streams {
		if stream.CodecType == "audio" {
			if stream.CodecName != ffmpeg.CodecAudioPCM && stream.CodecName != ffmpeg.CodecAudioPCM24b {
				return false
			}
		}
	}
	return true
}
