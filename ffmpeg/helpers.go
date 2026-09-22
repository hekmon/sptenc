package ffmpeg

import (
	"context"
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

// HWDecoderConfig holds hardware decoder selection settings.
type HWDecoderConfig struct {
	NVDec           bool
	NVDevice        int
	VAAPIDec        bool
	VAAPIDevice     string
	D3D12Dec        bool
	D3D12Device     int
	VideoToolboxDec bool
}

// SelectCompatibleDecoders probes the input file and returns an HWDecoderConfig
// with only the hardware decoders that are both requested and compatible.
func SelectCompatibleDecoders(ctx context.Context, inputPath string, wantNVDec, wantVAAPIDec, wantD3D12Dec, wantVideoToolboxDec bool, nvDevice int, vaDevice string, d3d12Device int) (dec HWDecoderConfig) {
	stats, err := GetStreamsInfos(ctx, GetStreamsInfosConfig{Path: inputPath})
	if err != nil {
		return
	}
	video := stats.VideoTrack()
	if video == nil {
		return
	}
	if wantNVDec && IsNVDecCompatible(video.CodecName) {
		dec.NVDec = true
		dec.NVDevice = nvDevice
	}
	if wantVAAPIDec && IsVAAPIDecCompatible(video.CodecName) {
		dec.VAAPIDec = true
		dec.VAAPIDevice = vaDevice
	}
	if wantD3D12Dec && IsD3D12DecCompatible(video.CodecName) {
		dec.D3D12Dec = true
		dec.D3D12Device = d3d12Device
	}
	if wantVideoToolboxDec && IsVideoToolboxDecCompatible(video.CodecName) {
		dec.VideoToolboxDec = true
	}
	return
}

// CompatibleWith returns the same decoder when it can decode the codec of the file (see
// SelectCompatibleDecoders), a software decode configuration otherwise.
func (dec HWDecoderConfig) CompatibleWith(ctx context.Context, inputPath string) HWDecoderConfig {
	return SelectCompatibleDecoders(ctx, inputPath, dec.NVDec, dec.VAAPIDec, dec.D3D12Dec, dec.VideoToolboxDec,
		dec.NVDevice, dec.VAAPIDevice, dec.D3D12Device)
}

// Enabled reports whether a hardware decoder is selected (software decode otherwise).
func (dec HWDecoderConfig) Enabled() bool {
	return dec.NVDec || dec.VAAPIDec || dec.D3D12Dec || dec.VideoToolboxDec
}

// Name returns the name of the selected hardware decoder, "software" when none is.
func (dec HWDecoderConfig) Name() string {
	switch {
	case dec.NVDec:
		return "NVDEC"
	case dec.VAAPIDec:
		return "VA-API"
	case dec.D3D12Dec:
		return "D3D12VA"
	case dec.VideoToolboxDec:
		return "VideoToolbox"
	default:
		return "software"
	}
}

// DecoderForEncoder returns the hardware decoder matching a hardware encoder (the same GPU),
// none for a CPU encoder. Whether the input codec can be decoded by it is not checked here,
// see CompatibleWith.
func DecoderForEncoder(encoder Encoder, nvidiaGPUIndex int, vaapiDevice string, d3d12GPUIndex int) (dec HWDecoderConfig) {
	dec = HWDecoderConfig{NVDevice: nvidiaGPUIndex, VAAPIDevice: vaapiDevice, D3D12Device: d3d12GPUIndex}
	switch encoder {
	case HEVCEncoderNVEnc, AV1EncoderNVEnc:
		dec.NVDec = true
	case HEVCEncoderVAAPI, AV1EncoderVAAPI:
		dec.VAAPIDec = true
	case HEVCEncoderD3D12VA:
		dec.D3D12Dec = true
	case HEVCEncoderVideoToolbox:
		dec.VideoToolboxDec = true
	}
	return
}

// ResolveHWDecoder picks the hardware decoder of an encoding run, by order of precedence:
//
//  1. the one matching the encoder when it is a hardware one (the same GPU is used both ways)
//  2. NVDEC when VMAF is computed on CUDA (the frames go to the GPU anyway)
//  3. the one explicitly requested, if any: a CPU encoder is chosen for its file size, not
//     because the machine has no accelerator, and every decode it does not do itself can be
//     taken away from the CPU it needs
//
// The devices always come from requested (they are the device flags). An explicit request
// contradicting the two first levels is an error: it can not be honored, better to say so than
// to decode with a hardware the user did not ask for.
func ResolveHWDecoder(encoder Encoder, vmafCUDA bool, requested HWDecoderConfig) (dec HWDecoderConfig, err error) {
	var origin string
	switch derived := DecoderForEncoder(encoder, requested.NVDevice, requested.VAAPIDevice, requested.D3D12Device); {
	case derived.Enabled():
		dec, origin = derived, fmt.Sprintf("the %s encoder", encoder)
	case vmafCUDA:
		dec, origin = derived, "VMAF on CUDA" // derived carries the devices only
		dec.NVDec = true
	default:
		return requested, nil
	}
	if requested.Enabled() && requested.Name() != dec.Name() {
		err = fmt.Errorf("%s decoding was requested but %s implies %s decoding", requested.Name(), origin, dec.Name())
	}
	return
}

// ToFFV1MasterConfig copies decoder settings into an FFV1VideoMasterConfig.
func (dec HWDecoderConfig) ToFFV1MasterConfig() FFV1VideoMasterConfig {
	return FFV1VideoMasterConfig{
		NVDec:           dec.NVDec,
		NVDevice:        dec.NVDevice,
		VAAPIDec:        dec.VAAPIDec,
		VAAPIDevice:     dec.VAAPIDevice,
		D3D12Dec:        dec.D3D12Dec,
		D3D12Device:     dec.D3D12Device,
		VideoToolboxDec: dec.VideoToolboxDec,
	}
}

// ToScenesDetectionConfig copies decoder settings into a ScenesDetectionConfig.
func (dec HWDecoderConfig) ToScenesDetectionConfig() ScenesDetectionConfig {
	return ScenesDetectionConfig{
		NVDec:           dec.NVDec,
		NVDevice:        dec.NVDevice,
		VAAPIDec:        dec.VAAPIDec,
		VAAPIDevice:     dec.VAAPIDevice,
		D3D12Dec:        dec.D3D12Dec,
		D3D12Device:     dec.D3D12Device,
		VideoToolboxDec: dec.VideoToolboxDec,
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
