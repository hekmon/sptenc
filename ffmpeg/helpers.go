package ffmpeg

import (
	"fmt"
	"os"
	"runtime"

	"al.essio.dev/pkg/shellescape"
	"github.com/hekmon/processpriority"
	"github.com/shirou/gopsutil/v4/cpu"
)

const (
	float64Precision = 64 // standard float64 precision for formatting
)

// Standard display resolution tiers used for encoder tiling and model selection.
const (
	Width4K   = 3840
	Height4K  = 2160
	WidthFHD  = 1920
	HeightFHD = 1080
)

var (
	// ProcessPriority will be applied to all commands ran by the ffmpeg package
	ProcessPriority = processpriority.BelowNormal
	// NbThreadsToUse is the number of CPU threads to use when auto threading can not be used
	NbThreadsToUse = runtime.NumCPU()
)

func init() {
	// Lower NbThreadsToUse to physical cores count instead of logical if possible
	physicalCores, err := cpu.Counts(false)
	if err != nil {
		fmt.Fprintf(os.Stderr, "WARNING: Failed to recover physical CPU cores count (will default to %d): %s\n", NbThreadsToUse, err)
	} else if physicalCores > 0 {
		NbThreadsToUse = physicalCores
	}
}

func getPrintableCMDLine(program string, args []string) string {
	return shellescape.Quote(program) + " " + shellescape.QuoteCommand(args)
}

// GetEncoderQPRange returns the QP range for the given encoder
func GetEncoderQPRange(encoder Encoder) (qpMin, qpMax int, found bool) {
	switch encoder {
	case HEVCEncoderLibx265:
		return HEVCLibx265QPMin, HEVCLibx265QPMax, true
	case HEVCEncoderNVEnc:
		return HEVCNVEncQPMin, HEVCNVEncQPMax, true
	case HEVCEncoderVAAPI:
		return HEVCVAAPIQPMin, HEVCVAAPIQPMax, true
	case HEVCEncoderD3D12VA:
		return HEVCD3D12VAQPMin, HEVCD3D12VAQPMax, true
	case HEVCEncoderVideoToolbox:
		return HEVCVideoToolboxQPMin, HEVCVideoToolboxQPMax, true
	case AV1EncoderLibaom:
		return AV1LibaomQPMin, AV1LibaomQPMax, true
	case AV1EncoderSVTAV1:
		return AV1SVTAV1QPMin, AV1SVTAV1QPMax, true
	case AV1EncoderNVEnc:
		return AV1NVEncQPMin, AV1NVEncQPMax, true
	case AV1EncoderVAAPI:
		return AV1VAAPIQPMin, AV1VAAPIQPMax, true
	default:
		return
	}
}

// IsNVDecCompatible reports whether the given codec can be decoded by NVDEC in
// principle. It performs a codec-level check only; it does not verify whether
// the specific GPU has the required silicon (e.g. AV1 NVDEC requires Ampere or
// newer, and some datacenter SKUs omit it entirely).
//
// Supported codecs: H.264, HEVC, MJPEG, MPEG-1/2/4, VP8/VP9, VC-1, AV1.
//
// References:
//   - https://docs.nvidia.com/video-technologies/video-codec-sdk/13.0/nvdec-video-decoder-api-prog-guide/index.html#supported-codecs
//   - https://trac.ffmpeg.org/wiki/HWAccelIntro#NVDECCUVID
func IsNVDecCompatible(codec CodecName) bool {
	switch codec {
	case CodecVideoMPEG1, CodecVideoMPEG2, CodecVideoMPEG4, CodecVideoVC1, CodecVideoAVC,
		CodecVideoHEVC, CodecVideoVP8, CodecVideoVP9, CodecVideoAV1, CodecVideoMJPEG:
		return true
	default:
		return false
	}
}

// IsVAAPIDecCompatible reports whether the given codec can be decoded by VA-API in
// principle. It performs a codec-level check only; actual support depends on the
// specific driver and hardware generation (e.g. AV1 VA-API decode requires Intel
// Xe-LP+ or AMD VCN3+).
//
// Supported codecs: H.264, HEVC, MPEG-2, VP9, VC-1, AV1.
//
// Reference: https://trac.ffmpeg.org/wiki/HWAccelIntro#VA-API
func IsVAAPIDecCompatible(codec CodecName) bool {
	switch codec {
	case CodecVideoMPEG2, CodecVideoVC1, CodecVideoAVC,
		CodecVideoHEVC, CodecVideoVP9, CodecVideoAV1:
		return true
	default:
		return false
	}
}

// IsD3D12DecCompatible reports whether the given codec can be decoded by D3D12VA
// in principle. It performs a codec-level check only; actual support depends on
// the specific GPU, driver, and Windows version (e.g. AV1 D3D12VA decode requires
// recent hardware, and the driver must expose decode tier 2 support).
//
// Supported codecs: H.264, HEVC, MPEG-2, VP9, VC-1, AV1.
//
// References:
//   - https://ffmpeg.org/doxygen/trunk/dir_3b1f69f89eda39a44baf4887988d54a7.html
//   - https://ffmpeg.org/ffmpeg-codecs.html
func IsD3D12DecCompatible(codec CodecName) bool {
	switch codec {
	case CodecVideoMPEG2, CodecVideoVC1, CodecVideoAVC,
		CodecVideoHEVC, CodecVideoVP9, CodecVideoAV1:
		return true
	default:
		return false
	}
}

// IsVideoToolboxDecCompatible reports whether the given codec can be decoded by
// VideoToolbox in principle. It performs a codec-level check only.
//
// Supported codecs: H.264, HEVC, MPEG-1, MPEG-2, MPEG-4 Part 2, ProRes.
// ProRes is omitted as sptenc targets consumer codecs.
//
// Reference: https://trac.ffmpeg.org/wiki/HWAccelIntro#VideoToolbox
func IsVideoToolboxDecCompatible(codec CodecName) bool {
	switch codec {
	case CodecVideoMPEG1, CodecVideoMPEG2, CodecVideoMPEG4,
		CodecVideoAVC, CodecVideoHEVC:
		return true
	default:
		return false
	}
}
