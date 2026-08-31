//go:build !windows

package ffmpeg

var (
	// FFProbeBinary is the path to the ffprobe executable.
	FFProbeBinary = "ffprobe"
	// FFMPEGBinary is the path to the ffmpeg executable.
	FFMPEGBinary = "ffmpeg"
)

func adaptVMAFPath(path string) string {
	// no issue on linux
	return path
}
