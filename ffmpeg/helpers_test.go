package ffmpeg

import "testing"

func TestResolveHWDecoder(t *testing.T) {
	devices := HWDecoderConfig{NVDevice: 1, VAAPIDevice: "/dev/dri/renderD129", D3D12Device: 2}
	requested := func(nvdec, vaapi, d3d12, videotoolbox bool) HWDecoderConfig {
		dec := devices
		dec.NVDec, dec.VAAPIDec, dec.D3D12Dec, dec.VideoToolboxDec = nvdec, vaapi, d3d12, videotoolbox
		return dec
	}
	for name, tc := range map[string]struct {
		encoder   Encoder
		vmafCUDA  bool
		requested HWDecoderConfig
		expected  string // Name of the resolved decoder
		fails     bool
	}{
		"CPU encoder, nothing requested":             {HEVCEncoderLibx265, false, devices, "software", false},
		"CPU encoder, NVDEC requested":               {HEVCEncoderLibx265, false, requested(true, false, false, false), "NVDEC", false},
		"CPU encoder, VideoToolbox requested":        {AV1EncoderSVTAV1, false, requested(false, false, false, true), "VideoToolbox", false},
		"GPU encoder decodes with its own GPU":       {HEVCEncoderNVEnc, false, devices, "NVDEC", false},
		"GPU encoder, same decoder requested":        {AV1EncoderVAAPI, false, requested(false, true, false, false), "VA-API", false},
		"GPU encoder, other decoder requested":       {HEVCEncoderNVEnc, false, requested(false, true, false, false), "", true},
		"VMAF on CUDA implies NVDEC":                 {HEVCEncoderLibx265, true, devices, "NVDEC", false},
		"VMAF on CUDA, NVDEC requested":              {HEVCEncoderLibx265, true, requested(true, false, false, false), "NVDEC", false},
		"VMAF on CUDA, other decoder requested":      {HEVCEncoderLibx265, true, requested(false, false, true, false), "", true},
		"GPU encoder prevails over VMAF on CUDA":     {HEVCEncoderVAAPI, true, devices, "VA-API", false},
		"no encoder (vmaf command), flags only":      {"", false, requested(false, false, true, false), "D3D12VA", false},
		"no encoder (vmaf command), CUDA and a flag": {"", true, requested(false, true, false, false), "", true},
	} {
		t.Run(name, func(t *testing.T) {
			dec, err := ResolveHWDecoder(tc.encoder, tc.vmafCUDA, tc.requested)
			if tc.fails {
				if err == nil {
					t.Fatalf("expected an error, got %s", dec.Name())
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if dec.Name() != tc.expected {
				t.Errorf("expected %s, got %s", tc.expected, dec.Name())
			}
			// devices always come from the flags
			if dec.NVDevice != devices.NVDevice || dec.VAAPIDevice != devices.VAAPIDevice || dec.D3D12Device != devices.D3D12Device {
				t.Errorf("devices lost: %+v", dec)
			}
		})
	}
}

// With the codec known, no file is probed: the path does not even need to exist.
func TestHWDecoderConfig_CompatibleWithKnownCodec(t *testing.T) {
	requested := HWDecoderConfig{NVDec: true, NVDevice: 1, VAAPIDevice: "/dev/dri/renderD129"}
	if dec := requested.compatibleWith(t.Context(), "does-not-exist.mkv", CodecVideoAVC, nil); dec != requested {
		t.Errorf("an H.264 file must keep NVDEC: got %+v", dec)
	}
	dec := requested.compatibleWith(t.Context(), "does-not-exist.mkv", CodecName("ffv1"), nil)
	if dec.Enabled() {
		t.Errorf("a FFV1 file must fall back to software decode: got %+v", dec)
	}
	if dec.NVDevice != 1 || dec.VAAPIDevice != "/dev/dri/renderD129" {
		t.Errorf("devices lost: %+v", dec)
	}
	// Nothing requested: nothing to check, whatever the codec
	if dec := (HWDecoderConfig{NVDevice: 1}).compatibleWith(t.Context(), "does-not-exist.mkv", "", nil); dec.Enabled() || dec.NVDevice != 1 {
		t.Errorf("unexpected change without any decoder requested: %+v", dec)
	}
}
