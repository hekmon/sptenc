package ffmpeg

import (
	"errors"
	"fmt"
)

// Colors are the colors a YUV stream declares, as ffprobe names them: its matrix (color_space), its
// range, its primaries and its transfer, each empty when it declares none.
type Colors struct {
	Matrix, Range, Primaries, Transfer string
}

// The values ffmpeg's colorspace filter converts from and to, under the names ffprobe prints (the
// filter takes them as they are): its matrices, primaries and transfers, the SDR ones. It knows no
// HDR transfer (smpte2084, arib-std-b67): bringing an HDR picture to an SDR one takes tone mapping.
var (
	colorspaceFilterMatrices  = []string{"bt709", "fcc", "bt470bg", "smpte170m", "smpte240m", "ycgco", "bt2020nc"}
	colorspaceFilterPrimaries = []string{"bt709", "bt470m", "bt470bg", "smpte170m", "smpte240m", "smpte428", "film",
		"smpte431", "smpte432", "bt2020", "jedec-p22", "ebu3213"}
	colorspaceFilterTransfers = []string{"bt709", "bt470m", "bt470bg", "smpte170m", "smpte240m", "linear",
		"iec61966-2-1", "iec61966-2-4", "bt2020-10", "bt2020-12"}
)

// ColorConversionFilter returns the filter converting YUV pictures declaring the colors from into the
// colors to: ffmpeg's colorspace filter, told every color of both sides. Every value must be given,
// the ones that do not change included (the same on both sides): the filter converts through RGB, and
// needs the matrix for a change of primaries, the transfer as well.
//
// # WHY NOT THE SCALE FILTER
//
// Given only another matrix, the scale filter (swscale) can hand the frames over untouched under the
// new label: converting BT.709 pictures into BT.601's matrix with it, from yuv420p10le to
// yuv420p10le, gave the very same pictures, declared BT.601 (measured with ffmpeg 9.0.2). The
// colorspace filter always converts.
//
// # EDGE CASES
//
//   - A conversion is not exact: the same RGB pictures converted into BT.709's matrix then by the
//     filter into BT.601's scored 98.8 (harmonic mean of fidelity) against their direct conversion
//     into BT.601's, on 1280x720 color bars, and BT.709 pictures converted by zscale into BT.601's
//     matrix 99.9 against BT.709 ones converted by the filter.
//   - Primaries and transfers are converted through linear light, scene-referred (the inverse of
//     the transfer's OETF): the filter's BT.709 to BT.2020 conversion matches ITU-R BT.2087's matrix
//     to the third decimal. Other tools convert differently (zscale, by default, gave other values),
//     so a file converted by one of them is not given back its source by the filter.
func ColorConversionFilter(from, to Colors) (filter string, err error) {
	for _, check := range []struct {
		property string
		known    []string
		values   [2]string
	}{
		{"matrix", colorspaceFilterMatrices, [2]string{from.Matrix, to.Matrix}},
		{"primaries", colorspaceFilterPrimaries, [2]string{from.Primaries, to.Primaries}},
		{"transfer", colorspaceFilterTransfers, [2]string{from.Transfer, to.Transfer}},
	} {
		for _, value := range check.values {
			if !knownColor(check.known, value) {
				return "", fmt.Errorf("ffmpeg's colorspace filter does not convert the %s %q", check.property, value)
			}
		}
	}
	for _, colorRange := range []string{from.Range, to.Range} {
		if colorRange != "tv" && colorRange != "pc" {
			return "", errors.New("ffmpeg's colorspace filter needs both ranges, tv or pc")
		}
	}
	return fmt.Sprintf("colorspace=ispace=%s:irange=%s:iprimaries=%s:itrc=%s:space=%s:range=%s:primaries=%s:trc=%s",
		from.Matrix, from.Range, from.Primaries, from.Transfer, to.Matrix, to.Range, to.Primaries, to.Transfer), nil
}

// CanConvertColor reports whether ffmpeg's colorspace filter converts from and to the value of a
// property: "matrix", "primaries" or "transfer".
func CanConvertColor(property, value string) bool {
	switch property {
	case "matrix":
		return knownColor(colorspaceFilterMatrices, value)
	case "primaries":
		return knownColor(colorspaceFilterPrimaries, value)
	case "transfer":
		return knownColor(colorspaceFilterTransfers, value)
	default:
		return false
	}
}

func knownColor(known []string, value string) bool {
	for _, k := range known {
		if value == k {
			return true
		}
	}
	return false
}
