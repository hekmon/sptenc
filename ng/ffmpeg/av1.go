package ffmpeg

const (
	// ffmpeg -h encoder=libaom-av1

	// AV1EncoderLibaom is the FFmpeg encoder name for libaom-av1 (AV1 software encoding).
	AV1EncoderLibaom Encoder = "libaom-av1"

	// AV1LibaomQPMin is the minimum Quantization Parameter (QP) value for libaom-av1.
	// It is applied via -crf because libaom-av1 does not expose a native -qp flag.
	AV1LibaomQPMin = 0
	// AV1LibaomQPMax is the maximum Quantization Parameter (QP) value for libaom-av1.
	// It is applied via -crf because libaom-av1 does not expose a native -qp flag.
	AV1LibaomQPMax = 63

	// libaom-av1 does not expose a native -qp flag via FFmpeg.
	// Its equivalent constant-QP mode is triggered with -crf X -b:v 0,
	// where -crf maps internally to aom's --cq-level under --end-usage=q.
	// The range documented here is therefore used as -crf, not -qp.
)

const (
	// ffmpeg -h encoder=av1_nvenc

	// AV1EncoderNVENC is the FFmpeg encoder name for NVIDIA NVENC AV1 hardware encoding.
	AV1EncoderNVEnc Encoder = "av1_nvenc"

	// AV1NVENCQPMin is the minimum Quantization Parameter (QP) value for av1_nvenc.
	AV1NVENCQPMin = 0
	// AV1NVENCQPMax is the maximum Quantization Parameter (QP) value for av1_nvenc.
	AV1NVENCQPMax = 255
)

const (
	// ffmpeg -h encoder=av1_vaapi

	// AV1EncoderVAAPI is the FFmpeg encoder name for VA-API AV1 hardware encoding.
	AV1EncoderVAAPI Encoder = "av1_vaapi"

	// AV1VAAPIQPMin is the minimum Quantization Parameter (QP) value for av1_vaapi.
	AV1VAAPIQPMin = 0
	// AV1VAAPIQPMax is the maximum Quantization Parameter (QP) value for av1_vaapi.
	AV1VAAPIQPMax = 255
)
