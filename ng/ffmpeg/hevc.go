package ffmpeg

const (
	// HEVCQPMin is the minimum Quantization Parameter (QP) value for HEVC encoders.
	HEVCQPMin = 0
	// HEVCQPMax is the maximum Quantization Parameter (QP) value for HEVC encoders.
	HEVCQPMax = 51
	// HEVCEncoderLibx265 is the FFmpeg encoder name for libx265 (HEVC software encoding).
	HEVCEncoderLibx265 Encoder = "libx265"
	// HEVCEncoderNVENC is the FFmpeg encoder name for NVIDIA NVENC HEVC hardware encoding.
	HEVCEncoderNVENC Encoder = "hevc_nvenc"
	// HEVCEncoderVAAPI is the FFmpeg encoder name for VA-API HEVC hardware encoding.
	HEVCEncoderVAAPI Encoder = "hevc_vaapi"
)
