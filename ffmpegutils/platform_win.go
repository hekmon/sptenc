//go:build windows

package ffmpegutils

import "strings"

const (
	FFProbeBinary = "ffprobe.exe"
	FFMPEGBinary  = "ffmpeg.exe"
)

func AdaptVMAFPath(path string) string {
	// https://github.com/Netflix/vmaf/blob/master/resource/doc/ffmpeg.md#note-about-the-model-path-on-windows
	return strings.Replace(strings.Replace(path, `\`, "/", -1), ":/", "\\\\:", 1)
}
