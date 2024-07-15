//go:build !windows

package ffmpegutils

const (
	FFProbeBinary = "ffprobe"
	FFMPEGBinary  = "ffmpeg"
)

func AdaptVMAFPath(path string) string {
	// no issue on linux
	return path
}
