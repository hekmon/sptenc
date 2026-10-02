package ffmpeg

import "strings"

// YUVMatrix names the matrix an RGB source is converted to YUV with, as ffprobe names the matrix
// a YUV stream declares (its color_space): the master declares it, and so do its encodes and the
// output (see RemuxColorSpace).
//
// # WHY AN EXPLICIT CONVERSION
//
// Every encoder sptenc drives takes YUV, and so does libvmaf: an RGB source has to be converted,
// with a matrix. Left to ffmpeg, the conversion happens wherever a YUV format is required (the
// pixel format of the master, the libvmaf filter, scdet), each time with the matrix the filter
// graph negotiates there. Nothing asks for one when the master is written: ffmpeg 9.0.2 converts
// to an unspecified matrix, which swscale computes with BT.601's coefficients, and the master
// declares no matrix. Measured on a saturated 1280x720 RGB clip declaring BT.709 primaries: the
// master was the BT.601 conversion bit for bit, under BT.709 primaries and transfer. Read with
// BT.709, the matrix those primaries go with, its pure green came out at 216 out of 255, its
// cyan's green at 230. Read with BT.601, as ffmpeg reads a missing matrix, it was right: sptenc's
// own VMAF saw nothing, the final one converting the source with BT.601 too.
//
// The matrix is no detail VMAF overlooks once both sides disagree: the RGB clip scored a harmonic
// mean of fidelity of 76.2 against its own BT.709 conversion declaring no matrix (ffmpeg converted
// the RGB side with BT.601), 70.6 against the BT.601 conversion declaring BT.709. So the source
// is converted once, with the matrix chosen here, and every step reading the source as YUV gets
// that very conversion (see RGBToYUVFilter): the master, the scene detection, the final VMAF.
//
// # WHY THE PRIMARIES DECIDE
//
// An RGB file declares no YUV matrix (its color_space is "gbr", the identity, when it declares
// one), but it declares primaries, and a YUV stream's matrix goes with its primaries: BT.709's
// with BT.709's, BT.2020's non-constant luminance one with BT.2020's (see
// YUVMatrixFromPrimaries). Other primaries, or none, leave the choice to the user: guessing from
// the resolution, as players guess the matrix of a YUV stream declaring none, would write a guess
// into the master and declare it as a fact.
//
// # WHY LIMITED RANGE AND LEFT CHROMA SITING
//
// Limited range is what the master holds for any source (see RemuxColorRange). The chroma is
// subsampled to 4:2:0 with the left siting, the one HEVC decoders assume when a stream declares
// none, and one of the two AV1 can declare. Left unset, swscale sites it in the center (its
// default, libswscale/format.c): against the same clip converted by zscale with the left siting,
// the center siting differed by 43.1 and 41.4 dB of PSNR on Cb and Cr, the left one by 51.3 and
// 49.6 dB.
//
// # EDGE CASES
//
//   - 16-bit RGB: swscale converts 8-bit white to 65283 in 16 bits, and that to 940, the white of
//     10-bit limited range. A 16-bit source whose white is 65535, as zscale writes it, comes out
//     3 levels brighter (943), whatever the swscale path (through 32-bit float or 12-bit RGB, or
//     with accurate rounding: measured). 8, 10 and 12-bit white comes out at 940. Converting with
//     zscale instead was rejected: ffmpeg builds do not all have it, and the master of the same
//     source would then depend on the build. A source converted to YUV upstream, with zscale, is
//     taken as it is.
//   - Alpha is dropped, as the master has no alpha plane.
type YUVMatrix string

// The matrices an RGB source can be converted with.
const (
	YUVMatrixBT709    YUVMatrix = "bt709"
	YUVMatrixBT2020NC YUVMatrix = "bt2020nc" // BT.2020 non-constant luminance
	YUVMatrixBT601    YUVMatrix = "bt470bg"  // BT.601's coefficients, the name ffmpeg declares them with
)

// YUVMatrices lists the matrices an RGB source can be converted with.
var YUVMatrices = []YUVMatrix{YUVMatrixBT709, YUVMatrixBT2020NC, YUVMatrixBT601}

// Valid reports whether the matrix is one an RGB source can be converted with.
func (m YUVMatrix) Valid() bool {
	for _, matrix := range YUVMatrices {
		if m == matrix {
			return true
		}
	}
	return false
}

// scaleName returns the name ffmpeg's scale filter gives the matrix (out_color_matrix): it knows
// BT.601's coefficients as bt601, not by the name a stream declares them with.
func (m YUVMatrix) scaleName() string {
	if m == YUVMatrixBT601 {
		return "bt601"
	}
	return string(m)
}

// YUVMatrixFromPrimaries returns the matrix that goes with the primaries ffprobe names
// (color_primaries), false for primaries that call for no matrix in particular (see YUVMatrix).
func YUVMatrixFromPrimaries(primaries string) (YUVMatrix, bool) {
	switch primaries {
	case "bt709":
		return YUVMatrixBT709, true
	case "bt2020":
		return YUVMatrixBT2020NC, true
	default:
		return "", false
	}
}

// IsRGBPixelFormat reports whether ffprobe's pixel format (pix_fmt) holds RGB pictures, which have
// to be converted to YUV with a matrix (see YUVMatrix). The names of ffmpeg 9.0.2's RGB pixel
// formats (ffprobe -show_pixel_formats, rgb flag) all start with one of these prefixes, and no
// other pixel format does. pal8 is counted in: its palette holds RGB colors.
func IsRGBPixelFormat(pixFmt string) bool {
	if pixFmt == "pal8" {
		return true
	}
	for _, prefix := range []string{"rgb", "bgr", "gbr", "argb", "abgr", "0rgb", "0bgr", "x2rgb", "x2bgr", "bayer_"} {
		if strings.HasPrefix(pixFmt, prefix) {
			return true
		}
	}
	return false
}

// RGBToYUVFilter returns the filters converting RGB pictures to the YUV of the master: the matrix,
// limited range, 4:2:0 with left chroma siting, 10 bits (see YUVMatrix). Every step reading an RGB
// source as YUV applies them, so that each gets the very pictures of the master.
func RGBToYUVFilter(matrix YUVMatrix) string {
	return "scale=out_color_matrix=" + matrix.scaleName() + ":out_range=tv:out_chroma_loc=left,format=" + masterPixelFormat
}

// IsFullChromaYUV reports whether ffprobe's pixel format (pix_fmt) holds YUV pictures whose chroma has
// the horizontal resolution of their luma (4:4:4, 4:4:0): the master subsamples it to 4:2:0 with the
// left siting (see FullChromaToMasterFilter). The names of ffmpeg 9.0.2's YUV pixel formats with full
// horizontal chroma (ffprobe -show_pixel_formats, log2_chroma_w 0) all start with one of these
// prefixes, and no other pixel format does.
func IsFullChromaYUV(pixFmt string) bool {
	for _, prefix := range []string{"yuv444", "yuva444", "yuvj444", "yuv440", "yuvj440", "nv24", "nv42", "ayuv", "vuya", "vuyx",
		"xv30", "xv36", "xv48", "p410", "p412", "p416", "uyva", "vyu444", "v30x"} {
		if strings.HasPrefix(pixFmt, prefix) {
			return true
		}
	}
	return false
}

// FullChromaToMasterFilter is the filter subsampling the chroma of full chroma YUV pictures (see
// IsFullChromaYUV) to the 4:2:0 of the master, with the left siting, the matrix kept.
//
// # WHY THE LEFT SITING
//
// Left unset, swscale sites the chroma it subsamples in the center (libswscale/format.c), and the
// master declares no siting: HEVC decoders then read it on the left, their default, half a pixel
// away. Measured on a 1280x720 4:4:4 clip, its chroma upsampled back on the left as those decoders
// do: 34.4 and 31.2 dB of PSNR against the source on Cb and Cr from a master sited in the center,
// 35.4 and 32.1 dB from one sited on the left. Sited in the center and declared so, it would come
// back better still (36.5 and 33.1 dB), but NVENC writes no siting in its stream and AV1 can not
// declare the center: the left is what HEVC decoders assume and what AV1 can declare.
//
// # EDGE CASES
//
//   - 4:2:2 sources are not touched: their chroma is subsampled vertically only, at the place of
//     their own (the default conversion gives the one of an explicit left to left siting).
//   - 4:1:1 sources are left to swscale, which sites them in the center as well: not handled.
const FullChromaToMasterFilter = "scale=out_chroma_loc=left"

// isYUV420 reports whether ffprobe's pixel format (pix_fmt) is a planar YUV 4:2:0 one, the format of
// the master and of every encode.
func isYUV420(pixFmt string) bool {
	return strings.HasPrefix(pixFmt, "yuv420") || strings.HasPrefix(pixFmt, "yuvj420") || strings.HasPrefix(pixFmt, "yuva420")
}
