package main

import (
	"fmt"
	"io"
	"strings"

	"github.com/hekmon/sptenc/ffmpeg"

	"github.com/urfave/cli/v3"
)

// vmafColors is how the vmaf command brings its two inputs to the same colors before VMAF compares
// them (see planVMAFColors).
type vmafColors struct {
	referenceRGBToYUV, distortedRGBToYUV ffmpeg.YUVMatrix
	referenceColorFilter                 string   // see ffmpeg.VMAFComputeConfig.ReferenceColorFilter
	warnings                             []string // what the user must know about the score
}

// planVMAFColors decides how the vmaf command brings its inputs to the same colors before VMAF
// compares them, and says so: the distorted video is scored as it is decoded, and the reference is
// brought to the colors the distorted video declares, its matrix, primaries and transfer, where
// both declare them and they differ. An RGB input is converted to YUV with the matrix the other input
// declares, else as encode converts an RGB source (see rgbInputMatrix), the RGB matrix flag first.
//
// # WHY THE COLORS THE FILES DECLARE
//
// What a file declares is what a player shows its pictures with, and all there is to tell what
// they are. Two files from anywhere can be converted into one another's colors and declare it: BT.709
// pictures converted into BT.601's matrix by zscale, and declared so, scored a harmonic mean of
// fidelity of 72.3 compared as they are, 99.9 once the reference was converted into BT.601's matrix
// too. A mislabelled file is scored as players show it, in other colors: the very same pictures
// declaring BT.601's matrix scored 77.4 against BT.709 ones. The encodes of sptenc are another
// matter: they declare the colors of their master, and encode compares them as they are (see
// ffmpeg.vmafSameMatrix).
//
// # WHY THE REFERENCE, NOT THE DISTORTED VIDEO
//
// The distorted video is the one scored: its pictures must stay as they are. The reference is
// brought to them, as the master of a source is brought to the format of its encodes, and as ffmpeg
// converts a range.
//
// # WHAT IS NOT CONVERTED
//
//   - A color one file declares and the other does not: nothing tells what to convert from or into,
//     the pictures are compared as they are, as if both declared the same. Declaring it in the file
//     is up to the user.
//   - Values ffmpeg's colorspace filter does not convert, HDR transfers among them: an HDR picture
//     against an SDR one takes tone mapping. Compared as they are, the score means little.
//   - A range alone: ffmpeg converts the range by itself, as it does for the final VMAF of encode,
//     the very conversion the master of a full range source went through.
//
// # EDGE CASES
//
//   - Primaries and transfers are converted as ffmpeg's colorspace filter does, an indication only:
//     tools convert them differently (BT.709 pictures converted into BT.2020's primaries by zscale
//     scored 38.7 as they are, 79.2 once the reference was converted by the filter, see
//     ffmpeg.ColorConversionFilter).
//   - The bt470bg and smpte170m matrices are the same coefficients, and the bt709, smpte170m and
//     bt2020 transfers the same curve: they are not told apart.
func planVMAFColors(cmd *cli.Command, out io.Writer, reference, distorted *ffmpeg.FFProbeBinaryStream) (plan vmafColors, err error) {
	ref, dist := declaredColors(reference), declaredColors(distorted)
	referenceRGB, distortedRGB := ffmpeg.IsRGBPixelFormat(reference.PixFmt), ffmpeg.IsRGBPixelFormat(distorted.PixFmt)
	forced := ffmpeg.YUVMatrix(cmd.String(rgbMatrixFlagName))
	if !referenceRGB && !distortedRGB && forced != "" {
		plan.warnings = append(plan.warnings, fmt.Sprintf("--%s has no effect: neither the reference (%s) nor the distorted video (%s) is RGB",
			rgbMatrixFlagName, reference.PixFmt, distorted.PixFmt))
	}
	// RGB inputs: the distorted video first, the reference then takes its matrix
	if distortedRGB {
		if plan.distortedRGBToYUV, err = rgbMatrixOf(cmd, out, distorted, "distorted video", ref.Matrix, !referenceRGB, "the reference"); err != nil {
			return
		}
		dist.Matrix, dist.Range = string(plan.distortedRGBToYUV), "tv"
	}
	if referenceRGB {
		if plan.referenceRGBToYUV, err = rgbMatrixOf(cmd, out, reference, "reference", dist.Matrix, true, "the distorted video"); err != nil {
			return
		}
		ref.Matrix, ref.Range = string(plan.referenceRGBToYUV), "tv"
	}
	// What differs, and what the colorspace filter can convert: a transfer or primaries need the
	// pictures in RGB, through a matrix, and primaries in linear light, through a transfer
	both := func(a, b string) bool { return a != "" && b != "" }
	matrixDiffers := both(ref.Matrix, dist.Matrix) && !sameMatrix(ref.Matrix, dist.Matrix)
	transferDiffers := both(ref.Transfer, dist.Transfer) && !sameTransfer(ref.Transfer, dist.Transfer)
	primariesDiffer := both(ref.Primaries, dist.Primaries) && ref.Primaries != dist.Primaries
	known := func(property string, values ...string) bool {
		for _, value := range values {
			if !ffmpeg.CanConvertColor(property, value) {
				return false
			}
		}
		return true
	}
	matrixConvertible := matrixDiffers && known("matrix", ref.Matrix, dist.Matrix)
	fromMatrix, toMatrix := firstDeclared(ref.Matrix, dist.Matrix), firstDeclared(ref.Matrix, dist.Matrix)
	if matrixConvertible {
		toMatrix = dist.Matrix
	}
	throughRGB := fromMatrix != "" && known("matrix", fromMatrix, toMatrix)
	convertTransfer := transferDiffers && throughRGB && known("transfer", ref.Transfer, dist.Transfer)
	// Pictures in different transfers that can not be brought together are left as they are
	bridged := !transferDiffers || convertTransfer
	convertMatrix := matrixConvertible && bridged
	if !convertMatrix {
		toMatrix = fromMatrix
	}
	fromTransfer, toTransfer := firstDeclared(ref.Transfer, dist.Transfer), firstDeclared(ref.Transfer, dist.Transfer)
	if convertTransfer {
		toTransfer = dist.Transfer
	}
	convertPrimaries := primariesDiffer && bridged && throughRGB && fromTransfer != "" && known("transfer", fromTransfer, toTransfer) &&
		known("primaries", ref.Primaries, dist.Primaries)
	// What is converted, and why not when it is not
	hdr := isHDRTransfer(ref.Transfer) || isHDRTransfer(dist.Transfer)
	for _, property := range []struct {
		name               string
		differs, converted bool
	}{{"matrix", matrixDiffers, convertMatrix}, {"transfer", transferDiffers, convertTransfer}, {"primaries", primariesDiffer, convertPrimaries}} {
		if !property.differs {
			continue
		}
		declares := fmt.Sprintf("the reference declares %s, the distorted video %s", describeColor(property.name, colorOf(ref, property.name)),
			describeColor(property.name, colorOf(dist, property.name)))
		var why string
		switch {
		case property.converted && property.name == "matrix":
			plan.warnings = append(plan.warnings, declares+": the reference is converted into the matrix of the distorted video before VMAF compares them, which is right if both declare it truly (a mislabelled file is scored as players show it, in other colors)")
			continue
		case property.converted:
			plan.warnings = append(plan.warnings, fmt.Sprintf("%s: the reference is converted into the %s of the distorted video with ffmpeg's colorspace filter, an indication only (tools convert %s differently, a file converted by another one does not get its source back)",
				declares, property.name, property.name))
			continue
		case property.name == "transfer" && hdr:
			why = "bringing an HDR picture to another takes tone mapping, which sptenc does not do"
		case property.name != "transfer" && !bridged:
			why = "their transfers can not be brought together, so neither can the rest"
		case property.name != "matrix" && !throughRGB:
			why = "converting it takes the matrix of the pictures, which they do not declare or ffmpeg's colorspace filter does not know"
		case property.name == "primaries" && fromTransfer == "":
			why = "converting primaries takes the transfer of the pictures, which they do not declare"
		case property.name == "primaries" && !known("transfer", fromTransfer, toTransfer):
			why = "ffmpeg's colorspace filter does not convert pictures in the " + fromTransfer + " transfer"
		default:
			why = "ffmpeg's colorspace filter does not convert it"
		}
		plan.warnings = append(plan.warnings, fmt.Sprintf("%s: %s, VMAF compares the pictures as they are, a score that means little", declares, why))
	}
	if convertMatrix || convertTransfer || convertPrimaries {
		// Every color is given to the filter: the ones not converted, the same on both sides, are
		// passed through (a placeholder for one nothing declares or the filter does not know)
		passed := func(property, value string) string {
			if value == "" || !ffmpeg.CanConvertColor(property, value) {
				return "bt709"
			}
			return value
		}
		from := ffmpeg.Colors{Matrix: fromMatrix, Range: rangeOrLimited(ref.Range), Transfer: passed("transfer", fromTransfer),
			Primaries: passed("primaries", firstDeclared(ref.Primaries, dist.Primaries))}
		to := ffmpeg.Colors{Matrix: toMatrix, Range: rangeOrLimited(dist.Range), Transfer: from.Transfer, Primaries: from.Primaries}
		if convertTransfer {
			from.Transfer, to.Transfer = ref.Transfer, dist.Transfer
		}
		if convertPrimaries {
			from.Primaries, to.Primaries = ref.Primaries, dist.Primaries
		}
		if plan.referenceColorFilter, err = ffmpeg.ColorConversionFilter(from, to); err != nil {
			err = fmt.Errorf("failed to convert the reference into the colors of the distorted video: %w", err)
			return
		}
	}
	if (rangeOrLimited(ref.Range) == "pc") != (rangeOrLimited(dist.Range) == "pc") {
		rangeOf := func(name, colorRange string, converted bool) string {
			switch {
			case converted:
				return name + " is converted from RGB to limited range"
			case colorRange == "pc":
				return name + " declares full range (pc)"
			case colorRange == "tv":
				return name + " declares limited range (tv)"
			default:
				return name + " declares no range, which ffmpeg reads as limited"
			}
		}
		plan.warnings = append(plan.warnings, fmt.Sprintf("%s, %s: the reference is converted into the range of the distorted video before VMAF compares them, which is right only if both declare it truly",
			rangeOf("the reference", ref.Range, referenceRGB), rangeOf("the distorted video", dist.Range, distortedRGB)))
	}
	// What one declares and the other does not
	for _, pair := range [][2]struct {
		name   string
		colors ffmpeg.Colors
	}{{{"the distorted video", dist}, {"the reference", ref}}, {{"the reference", ref}, {"the distorted video", dist}}} {
		lacking, other := pair[0], pair[1]
		var names, values []string
		for _, property := range []string{"matrix", "primaries", "transfer"} {
			if colorOf(lacking.colors, property) == "" && colorOf(other.colors, property) != "" {
				names, values = append(names, property), append(values, colorOf(other.colors, property))
			}
		}
		if len(names) > 0 {
			plan.warnings = append(plan.warnings, fmt.Sprintf("%s declares no %s, where %s declares %s: VMAF compares the pictures as they are, as if it declared the same. If it does not, declare them in its file (ffmpeg's -colorspace, -color_primaries and -color_trc, with -c copy) and run again",
				lacking.name, joinWords(names, "or"), other.name, joinWords(values, "and")))
		}
	}
	return
}

// rgbMatrixOf returns the matrix an RGB input, named what, is converted to YUV with, and says so: the
// one forced with the RGB matrix flag, the one the other input declares (otherMatrix, when useOther),
// the one its primaries call for otherwise (see rgbInputMatrix).
func rgbMatrixOf(cmd *cli.Command, out io.Writer, stream *ffmpeg.FFProbeBinaryStream, what, otherMatrix string, useOther bool,
	otherName string) (matrix ffmpeg.YUVMatrix, err error) {
	if cmd.String(rgbMatrixFlagName) == "" && useOther {
		if matrix = yuvMatrixOf(otherMatrix); matrix != "" {
			announceRGBConversion(out, what, stream.PixFmt, matrix, "declared by "+otherName)
			return
		}
	}
	return rgbInputMatrix(cmd, out, stream, what)
}

// yuvMatrixOf returns the matrix an RGB input can be converted with for a matrix as ffprobe names it,
// empty for none of them.
func yuvMatrixOf(matrix string) ffmpeg.YUVMatrix {
	switch matrix {
	case "bt709":
		return ffmpeg.YUVMatrixBT709
	case "bt2020nc":
		return ffmpeg.YUVMatrixBT2020NC
	case "bt470bg", "smpte170m":
		return ffmpeg.YUVMatrixBT601
	default:
		return ""
	}
}

// declaredColors returns the colors a stream declares, the unknown and reserved values left out.
func declaredColors(stream *ffmpeg.FFProbeBinaryStream) ffmpeg.Colors {
	declared := func(value string) string {
		if value == "unknown" || value == "reserved" {
			return ""
		}
		return value
	}
	return ffmpeg.Colors{Matrix: declared(stream.ColorSpace), Range: declared(stream.ColorRange),
		Primaries: declared(stream.ColorPrimaries), Transfer: declared(stream.ColorTransfer)}
}

// colorOf returns the value of a property of colors: "matrix", "primaries" or "transfer".
func colorOf(colors ffmpeg.Colors, property string) string {
	switch property {
	case "matrix":
		return colors.Matrix
	case "primaries":
		return colors.Primaries
	default:
		return colors.Transfer
	}
}

// describeColor names the value of a property as a sentence does: "the bt709 matrix", "bt709
// primaries", "the bt709 transfer".
func describeColor(property, value string) string {
	if property == "primaries" {
		return value + " primaries"
	}
	return "the " + value + " " + property
}

func sameValue(a, b string) bool { return a == b }

// sameMatrix reports whether two matrices are the same coefficients.
func sameMatrix(a, b string) bool {
	canonical := func(matrix string) string {
		if matrix == "smpte170m" {
			return "bt470bg"
		}
		return matrix
	}
	return canonical(a) == canonical(b)
}

// sameTransfer reports whether two transfers are the same curve: the SDR ones of BT.709, BT.601
// (smpte170m) and BT.2020 are.
func sameTransfer(a, b string) bool {
	canonical := func(transfer string) string {
		switch transfer {
		case "smpte170m", "bt2020-10", "bt2020-12":
			return "bt709"
		}
		return transfer
	}
	return canonical(a) == canonical(b)
}

// isHDRTransfer reports whether a transfer, as ffprobe names it, is an HDR one.
func isHDRTransfer(transfer string) bool {
	return transfer == "smpte2084" || transfer == "arib-std-b67"
}

// firstDeclared returns the first value declared, empty when none is.
func firstDeclared(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

// rangeOrLimited returns the range a stream declares, limited when it declares none: ffmpeg reads a
// missing range as limited.
func rangeOrLimited(colorRange string) string {
	if colorRange == "" {
		return "tv"
	}
	return colorRange
}

// joinWords joins words as a sentence lists them: "a", "a or b", "a, b or c".
func joinWords(words []string, conjunction string) string {
	if len(words) < 2 {
		return strings.Join(words, "")
	}
	return strings.Join(words[:len(words)-1], ", ") + " " + conjunction + " " + words[len(words)-1]
}
