package core

import (
	"fmt"
	"os"

	"github.com/hekmon/sptenc/ng/ffmpeg"

	"github.com/hekmon/cunits/v3"
)

func getFileSize(path string) (size cunits.Bits, err error) {
	info, err := os.Stat(path)
	if err != nil {
		err = fmt.Errorf("failed to stat path: %w", err)
	} else {
		size = cunits.ImportInBytes(float64(info.Size()))
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
