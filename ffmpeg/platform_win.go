//go:build windows

package ffmpeg

import "strings"

var (
	// FFProbeBinary is the path to the ffprobe executable.
	FFProbeBinary = ".\\ffprobe.exe"
	// FFMPEGBinary is the path to the ffmpeg executable.
	FFMPEGBinary = ".\\ffmpeg.exe"
)

func adaptVMAFPath(path string) string {
	// https://github.com/Netflix/vmaf/blob/master/resource/doc/ffmpeg.md#note-about-the-model-path-on-windows
	return strings.ReplaceAll(strings.ReplaceAll(path, `\`, "/"), ":/", `\\:/`)
}
