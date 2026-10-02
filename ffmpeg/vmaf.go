package ffmpeg

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hekmon/processpriority"
	"github.com/olekukonko/tablewriter"
	"github.com/olekukonko/tablewriter/renderer"
	"github.com/olekukonko/tablewriter/tw"
)

/*
 * Models
 */

// VMAFModel names a VMAF model built into libvmaf. sptenc selects between two VMAF v1 models
// (libvmaf 3.2.0 or newer, see VMAFModels) and scores with any other one libvmaf knows when the
// user forces it: whether libvmaf knows a name is for VMAFProbe to tell, so a model released
// after sptenc can be used without waiting for an update. The v1 models fuse ADM (with an
// additive impairment term for blockiness), motion, CAMBI (banding) and a chroma feature; the
// enhancement gain is clamped in all of them (what used to be the NEG variants of v0), and VIF
// is gone. See resource/doc/models_v1.md in the libvmaf repository.
type VMAFModel string

const (
	// VMAFModelFHD predicts the viewing condition of a 1080p display watched from 3 picture
	// heights: the v1 successor of vmaf_v0.6.1.
	VMAFModelFHD VMAFModel = "vmaf_v1.0.16_3d0h"
	// VMAFModelUHD predicts the viewing condition of a 2160p display watched from 1.5 picture
	// heights, the distance at which 4K is worth it: the v1 successor of vmaf_4k_v0.6.1.
	VMAFModelUHD VMAFModel = "vmaf_v1.0.16_1d5h_2160"
	// The other v1 models libvmaf builds in (all of them since 3.2.0), never selected: named
	// for the messages of a run that forces one (see VMAFForcedModels).
	VMAFModelPhone     VMAFModel = "vmaf_v1.0.16_5d0h"
	VMAFModelUHDFar    VMAFModel = "vmaf_v1.0.16_3d0h_2160"
	VMAFModelFHDHFR    VMAFModel = "vmaf_v1.0.16_hfr_3d0h"
	VMAFModelUHDHFR    VMAFModel = "vmaf_v1.0.16_hfr_1d5h_2160"
	VMAFModelPhoneHFR  VMAFModel = "vmaf_v1.0.16_hfr_5d0h"
	VMAFModelUHDFarHFR VMAFModel = "vmaf_v1.0.16_hfr_3d0h_2160"
	// The v0 models sptenc v0.1.0 selected, never selected anymore: named for the messages of
	// a run that forces one (see VMAFV0Models).
	VMAFModelV0FHD    VMAFModel = "vmaf_v0.6.1"
	VMAFModelV0FHDNEG VMAFModel = "vmaf_v0.6.1neg"
	VMAFModelV0UHD    VMAFModel = "vmaf_4k_v0.6.1"
	VMAFModelV0UHDNEG VMAFModel = "vmaf_4k_v0.6.1neg"
)

// VMAFModels lists the models sptenc selects, from the height of the source (see
// SelectVMAFModel), and the ones it checks and lists: the viewing conditions where the small
// artifacts a quality floor is about are seen, at the frame rates the models are made for.
var VMAFModels = []VMAFModel{VMAFModelFHD, VMAFModelUHD}

// VMAFForcedModels lists the other v1 models libvmaf builds in, which sptenc never selects:
// the phone (5 picture heights) and the 4K at 3 picture heights viewing conditions are
// lenient, small artifacts are not seen from there (the latter also scores up to 110, above
// what a threshold can ask for), and the high frame rate variants, made for ~50/60 fps, are in
// Netflix's words "an area of active improvement". Forcing one is the user's call.
var VMAFForcedModels = []VMAFModel{VMAFModelPhone, VMAFModelUHDFar,
	VMAFModelFHDHFR, VMAFModelUHDHFR, VMAFModelPhoneHFR, VMAFModelUHDFarHFR}

// VMAFV0Models lists the v0 models, which sptenc v0.1.0 selected and libvmaf still builds in
// (the 4K NEG one since 3.0). sptenc never selects them: they measure luma only and do not see
// banding (their NEG variants barely do). Forcing one scores like the published VMAF anchors
// were measured, or like a v0.1.0 encode.
var VMAFV0Models = []VMAFModel{VMAFModelV0FHD, VMAFModelV0FHDNEG, VMAFModelV0UHD, VMAFModelV0UHDNEG}

// IsV0 reports whether the model is one of the v0 models (see VMAFV0Models).
func (m VMAFModel) IsV0() bool {
	for _, candidate := range VMAFV0Models {
		if m == candidate {
			return true
		}
	}
	return false
}

// vmafModelNameRe is what a model name may be made of: it is written into an ffmpeg filter
// graph, where ':', '=', ',' or ';' would be read as options or filters.
var vmafModelNameRe = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// String returns the model name as libvmaf knows it.
func (m VMAFModel) String() string {
	return string(m)
}

// Valid reports whether the name can be handed to libvmaf. Whether libvmaf knows the model is
// for VMAFProbe to tell: any model it knows is accepted, a future one included.
func (m VMAFModel) Valid() bool {
	return vmafModelNameRe.MatchString(string(m))
}

// Description returns the viewing condition the model predicts, empty for a model sptenc does
// not know.
func (m VMAFModel) Description() string {
	switch m {
	case VMAFModelFHD:
		return "1080p display at 3 picture heights"
	case VMAFModelUHD:
		return "2160p display at 1.5 picture heights"
	case VMAFModelPhone:
		return "phone (1080p) at 5 picture heights"
	case VMAFModelUHDFar:
		return "2160p display at 3 picture heights, scored up to 110"
	case VMAFModelFHDHFR:
		return "1080p display at 3 picture heights, ~50/60 fps"
	case VMAFModelUHDHFR:
		return "2160p display at 1.5 picture heights, ~50/60 fps"
	case VMAFModelPhoneHFR:
		return "phone (1080p) at 5 picture heights, ~50/60 fps"
	case VMAFModelUHDFarHFR:
		return "2160p display at 3 picture heights, ~50/60 fps, scored up to 110"
	case VMAFModelV0FHD:
		return "1080p display at 3 picture heights"
	case VMAFModelV0FHDNEG:
		return "1080p display at 3 picture heights, no enhancement gain"
	case VMAFModelV0UHD:
		return "2160p display at 1.5 picture heights"
	case VMAFModelV0UHDNEG:
		return "2160p display at 1.5 picture heights, no enhancement gain"
	default:
		return ""
	}
}

// displayHeight returns the height of the display the model predicts: 2160 for the 4K models,
// 1080 for the others (the phone ones included), 0 for a model sptenc does not know.
func (m VMAFModel) displayHeight() int {
	switch m {
	case VMAFModelUHD, VMAFModelUHDFar, VMAFModelUHDHFR, VMAFModelUHDFarHFR,
		VMAFModelV0UHD, VMAFModelV0UHDNEG:
		return Height4K
	case VMAFModelFHD, VMAFModelPhone, VMAFModelFHDHFR, VMAFModelPhoneHFR,
		VMAFModelV0FHD, VMAFModelV0FHDNEG:
		return 1080
	default:
		return 0
	}
}

// SelectVMAFModel returns the model matching the resolution of a source: the 4K model from
// 2160 lines up, the 1080p model below.
func SelectVMAFModel(height int) VMAFModel {
	if height >= Height4K {
		return VMAFModelUHD
	}
	return VMAFModelFHD
}

// ResolutionMismatch explains why the model is not made for the display of a source of the
// given height (see SelectVMAFModel), an empty string when it is, or when sptenc does not know
// the model. Only the display is compared: the viewing distance and the frame rate of a forced
// model are the user's choice.
func (m VMAFModel) ResolutionMismatch(height int) string {
	expected := SelectVMAFModel(height)
	if display := m.displayHeight(); display == 0 || display == expected.displayHeight() {
		return ""
	}
	return fmt.Sprintf("%s (%s) is made for another display than this %dp source, %s is the model of this resolution",
		m, m.Description(), height, expected)
}

/*
 * Pass
 */

// VMAFScore names one of the two scores sptenc computes with a model fed with CAMBI.
//
// # WHY TWO SCORES
//
// A v1 model scores two different things with one number. Its full-reference features (ADM and
// its additive impairment term, motion, chroma) measure what the encode lost against its source.
// CAMBI, fed to the same model, rates the banding of the distorted picture alone: the banding
// already in the source counts as if the encoder had made it, and a banded source scores below
// 100 against itself (95.89 for the dark gradient of BENCHMARKS.md). Fidelity is the score of the
// same model with its CAMBI clipped to 0: 100 for a source against itself, what the encode lost
// otherwise. The banding the encoder added is measured apart (see VMAFMeasures.Banding). A model
// without CAMBI (a v0 one) has a single score, which is both. See AGENTS.md, "VMAF fidelity and
// added banding".
type VMAFScore string

const (
	// VMAFScoreFidelity is the model's score with its CAMBI clipped to 0.
	VMAFScoreFidelity VMAFScore = "fidelity"
	// VMAFScoreOriginal is the model's own score, CAMBI included, as libvmaf computes it.
	VMAFScoreOriginal VMAFScore = "original"
)

// VMAFMeasures is what a libvmaf pass computes.
type VMAFMeasures struct {
	Fidelity bool // The model's score with its CAMBI clipped to 0 (see VMAFScoreFidelity).
	Original bool // The model's own score (see VMAFScoreOriginal).
	// Banding adds CAMBI in full-reference mode, fed to no model: per frame, the banding of the
	// distorted picture, of the reference, and what the first adds to the second (see
	// VMAFBanding). Alone, the pass loads no model.
	Banding bool
}

// libvmafParamSeparator separates the parameters of a model or of a feature in the model and
// feature options of the libvmaf filter: a ':' escaped for the option value (\:), then again for
// the filtergraph (\\:), as ffmpeg's documentation of the filter writes it (see vmafPathEscaper).
const libvmafParamSeparator = `\\:`

// cambiClipParam clips the CAMBI a model is fed with to 0, for its fidelity score. A model
// parameter "<feature>.<option>" sets an option of one of the model's features (ffmpeg's
// vf_libvmaf.c hands it to vmaf_model_feature_overload), and cambi_max_val caps the CAMBI of the
// feature. A model without CAMBI has no such feature, and libvmaf ignores the parameter without a
// word, ffmpeg exiting successfully: whether a model feeds on CAMBI is for VMAFCAMBIProbe to tell,
// from its report.
const cambiClipParam = "cambi.cambi_max_val=0"

// bandingFeature is the CAMBI extractor measuring the banding the distorted video adds to the
// reference: CAMBI in full-reference mode (libvmaf 2.3.1 and newer), with the options of the
// v1.0.16 models (the eight of them share them) without their clip.
//
// # WHY NOT THE OPTIONS OF THE MODELS, CLIP INCLUDED
//
// The models clip their CAMBI at 17 (cambi_max_val=17). libvmaf keys a feature by a name built
// from the options that are feature parameters, and full_ref is not one (libvmaf's
// src/feature/cambi.c): given the very options of a model in the same pass, the extractor is taken
// for the model's CAMBI, and the source's CAMBI and the added banding are gone from the report
// (verified with libvmaf f85a8536, a September 2026 build). Without the clip, the options differ
// from those of the v1.0.16 models, clipped to 0 for fidelity or at 17 for the original score, and
// the CAMBI of the distorted picture is not capped at 17 (libvmaf's default cap is 1000): the
// 8-bit-like steps of BENCHMARKS.md rate 22.48, where the models see 17. A model whose CAMBI would
// have the options of the feature takes it the same way: the report lacks the source's CAMBI and
// the added banding, which readVMAFReport refuses.
//
// # WHY THE SPEEDUP OF THE MODELS
//
// cambi_high_res_speedup=1080 downsamples the pictures of 1920×1080 pixels and more (smaller ones
// are computed at full resolution), and libvmaf's documentation expects "some loss of accuracy".
// Against full resolution, on the 776 segment encodes of BENCHMARKS.md, the means of a segment
// stay within 0.085 of each other, never on opposite sides of 0.5, 1, 2 or 3. Full resolution
// rates grain on a near-black background as banding in the source, and then sees none added where
// an encoder turned that grain into flat blocks, which the speedup flags. It is also what the
// models see.
const bandingFeature = "name=cambi" +
	libvmafParamSeparator + "full_ref=true" +
	libvmafParamSeparator + "cambi_high_res_speedup=1080" +
	libvmafParamSeparator + "cambi_vis_lum_threshold=0.06"

// Report keys of bandingFeature. The key of the distorted picture's CAMBI is built by libvmaf
// from the options of the feature (hrs for cambi_high_res_speedup, vlt for
// cambi_vis_lum_threshold): it goes with bandingFeature.
const (
	bandingEncodeKey = "cambi_hrs_1080_vlt_0.06"
	bandingSourceKey = "cambi_source"
	bandingAddedKey  = "cambi_full_reference"
)

// Report keys of the scores: "vmaf" when a pass loads a single model. Two models in a pass must
// be named, and their scores are then keyed by their names (see vmafPass.filter).
const vmafScoreKey = "vmaf"

// cambiMetricPrefix starts the key of any CAMBI feature (see modelCAMBIKeys).
const cambiMetricPrefix = "cambi"

// vmafPass describes a libvmaf pass. The filter is built from it (see filter), and its report is
// read with it (see readVMAFReport): the report holds the keys the pass asks for.
type vmafPass struct {
	model      VMAFModel
	modelCAMBI bool // the model feeds on CAMBI (see VMAFCAMBIProbe)
	measures   VMAFMeasures
}

// validate refuses a pass measuring nothing, and a model name that can not be handed to libvmaf.
func (p vmafPass) validate() error {
	if !p.measures.Fidelity && !p.measures.Original && !p.measures.Banding {
		return errors.New("the VMAF pass measures nothing: a score or the banding must be asked for")
	}
	if clipped, asIs := p.models(); (clipped || asIs) && !p.model.Valid() {
		return fmt.Errorf("invalid VMAF model name %q", p.model)
	}
	return nil
}

// models returns the models the pass loads: clipped for fidelity, as is for the original score. A
// model without CAMBI is loaded once, as is, whichever score is asked: its score is both, and
// libvmaf would ignore the clip anyway (see cambiClipParam).
func (p vmafPass) models() (clipped, asIs bool) {
	if !p.modelCAMBI {
		return false, p.measures.Fidelity || p.measures.Original
	}
	return p.measures.Fidelity, p.measures.Original
}

// scoreKeys returns the report keys of the fidelity and of the original score, empty for a score
// the pass does not compute. A model without CAMBI has the same key for both: it is loaded once.
func (p vmafPass) scoreKeys() (fidelity, original string) {
	switch clipped, asIs := p.models(); {
	case clipped && asIs:
		return string(VMAFScoreFidelity), string(VMAFScoreOriginal)
	case clipped:
		return vmafScoreKey, ""
	case asIs && !p.modelCAMBI:
		return vmafScoreKey, vmafScoreKey
	case asIs:
		return "", vmafScoreKey
	default:
		return "", ""
	}
}

// filter returns the libvmaf filter of the pass, to be fed the distorted stream then the
// reference one. A model loaded alone is not named, its score keyed "vmaf" as libvmaf does by
// default: a pass computing the original score alone is the filter sptenc v0.1.0 ran.
func (p vmafPass) filter(reportPath string, threads int) string {
	clippedModel := "version=" + p.model.String() + libvmafParamSeparator + cambiClipParam
	asIsModel := "version=" + p.model.String()
	var models string
	switch clipped, asIs := p.models(); {
	case clipped && asIs:
		models = clippedModel + libvmafParamSeparator + "name=" + string(VMAFScoreFidelity) + "|" +
			asIsModel + libvmafParamSeparator + "name=" + string(VMAFScoreOriginal)
	case clipped:
		models = clippedModel
	case asIs:
		models = asIsModel
	}
	var feature string
	if p.measures.Banding {
		feature = ":feature=" + bandingFeature
	}
	return fmt.Sprintf("libvmaf=model=%s%s:log_fmt=json:log_path=%s:n_threads=%d",
		models, feature, adaptVMAFPath(reportPath), threads)
}

/*
 * Probe
 */

// ErrVMAFModelUnavailable is returned by VMAFProbe and VMAFCAMBIProbe when the libvmaf of ffmpeg
// does not know the model (the v1 models are built into libvmaf 3.2.0 and newer).
var ErrVMAFModelUnavailable = errors.New("libvmaf does not know this model")

// Probe pictures: 1080p unless told otherwise, the resolution the models are made for (smaller
// pictures are not scored by every model, see VMAFProbeConfig), and the fewest frames a report
// can be built from.
const (
	vmafProbeWidth    = 1920
	vmafProbeHeight   = 1080
	vmafProbeFrames   = 2
	vmafProbeFileMask = "sptenc-vmaf-probe-*.json"
)

// vmafModelUnavailableMarker is what ffmpeg prints when libvmaf can not load a model by its
// version name.
const vmafModelUnavailableMarker = "could not load libvmaf model with version"

// vmafProbePictures returns the lavfi source of the pictures of a probe: ffmpeg's testsrc2.
func vmafProbePictures(width, height int) string {
	return fmt.Sprintf("testsrc2=size=%dx%d:rate=%d:duration=1", width, height, vmafProbeFrames)
}

// vmafProbeBandedPictures is the lavfi source of banded probe pictures: the dark gradient of
// BENCHMARKS.md, one 10-bit code every 12 pixels from 64, in 1080p. The CAMBI of the v1.0.16
// models rates it 6.65, where testsrc2 rates 0.03 to 0.05: a clip to 0 is seen to take on it (see
// VMAFCAMBIProbe).
var vmafProbeBandedPictures = fmt.Sprintf(
	"color=black:size=%dx%d:rate=%d:duration=1,format=yuv420p10le,geq=lum='64+floor(X/12)':cb=512:cr=512",
	vmafProbeWidth, vmafProbeHeight, vmafProbeFrames)

// VMAFProbeConfig holds the parameters of a libvmaf probe.
type VMAFProbeConfig struct {
	Model      VMAFModel    // Model to load.
	ModelCAMBI bool         // Whether the model feeds on CAMBI (see VMAFCAMBIProbe).
	Measures   VMAFMeasures // What the pass measures: the probe runs the very pass a run uses.
	// Size of the probe pictures, 1920x1080 when zero. Below a minimum size libvmaf crashes, or
	// writes no report, or a report without some of the metrics asked for, ffmpeg exiting
	// successfully, and that minimum depends on the model, on the aspect ratio and on the pass,
	// which a fixed floor can not follow. Measured with libvmaf f85a8536 (a September 2026 build):
	// the 1080p and 4K models of VMAFModels score 216x160 but not 1920x160 nor 3840x180, the phone
	// models need 480x270 and the 4K at 3 picture heights ones 568x320 at 16:9. CAMBI, in the
	// models and in the banding feature alike, measures nothing when the width and the height are
	// both under 216 pixels (libvmaf's src/feature/cambi.c): a v0 model scores 215x160, but the
	// banding feature beside it reports nothing. Probing with the size of the source and the
	// passes of the run tells for sure, whatever the model and the libvmaf version.
	Width, Height int
	ReportDir     string           // Directory the JSON report of the probe is written to (and removed from).
	Debug         func(msg string) // Optional debug logger.
}

// VMAFProbe checks that the libvmaf of ffmpeg can run a pass on pictures of a given size, and
// returns the libvmaf version. It runs libvmaf for real on two synthetic frames generated by
// ffmpeg itself (no file needed), scored against themselves. Success is a report on disk holding
// every metric of the pass for every frame: the exit code of ffmpeg is not to be trusted,
// libvmaf errors have been seen leaving it at zero with no report written, or an incomplete one.
// A model libvmaf does not know is reported with ErrVMAFModelUnavailable.
func VMAFProbe(ctx context.Context, config VMAFProbeConfig) (libvmafVersion string, err error) {
	pass := vmafPass{model: config.Model, modelCAMBI: config.ModelCAMBI, measures: config.Measures}
	if err = pass.validate(); err != nil {
		return
	}
	if config.Width == 0 || config.Height == 0 {
		config.Width, config.Height = vmafProbeWidth, vmafProbeHeight
	}
	report, err := runVMAFProbe(ctx, vmafProbePictures(config.Width, config.Height), pass, config.ReportDir, config.Debug)
	if err != nil {
		return
	}
	if libvmafVersion = report.Version; libvmafVersion == "" {
		libvmafVersion = "unknown"
	}
	return
}

// ErrVMAFCAMBIClip is returned by VMAFCAMBIProbe when libvmaf does not clip the CAMBI of a model
// feeding on it: its fidelity score can not be computed.
var ErrVMAFCAMBIClip = errors.New("libvmaf does not clip the CAMBI of this model")

// VMAFCAMBIProbeConfig holds the parameters of VMAFCAMBIProbe.
type VMAFCAMBIProbeConfig struct {
	Model     VMAFModel        // Model to load.
	ReportDir string           // Directory the JSON reports of the probe are written to (and removed from).
	Debug     func(msg string) // Optional debug logger.
}

// VMAFCAMBIProbe tells whether a model feeds on CAMBI, and checks that libvmaf clips it for the
// fidelity score (see VMAFScore). Everything that depends on the model having CAMBI follows this
// answer: the pass of each score, whether the CAMBI gate is on by default, what is printed,
// tagged and cached.
//
// # WHY THE REPORT, NOT FFMPEG'S SUCCESS
//
// A model without CAMBI (the v0 ones) takes the clip without a word (see cambiClipParam): ffmpeg
// succeeds either way. What tells is the report: a model feeding on CAMBI reports its CAMBI
// feature, one without reports none.
//
// # WHY TWO PASSES ON A BANDED PICTURE
//
// The model is run as is, then clipped. As is, its CAMBI feature is in the report or not, which
// is the answer. Clipped, the feature is still reported, all zeros: that proves the clip took
// only if the same picture rates above zero as is, hence the banded gradient of
// vmafProbeBandedPictures (6.65 for the v1.0.16 models, where testsrc2 rates 0.03 to 0.05, too
// close to 0 to count on for another model). The first pass fails on a model libvmaf does not know
// (ErrVMAFModelUnavailable), the second on a clip that does not take (ErrVMAFCAMBIClip).
func VMAFCAMBIProbe(ctx context.Context, config VMAFCAMBIProbeConfig) (modelCAMBI bool, err error) {
	asIs := vmafPass{model: config.Model, measures: VMAFMeasures{Original: true}}
	if err = asIs.validate(); err != nil {
		return
	}
	report, err := runVMAFProbe(ctx, vmafProbeBandedPictures, asIs, config.ReportDir, config.Debug)
	if err != nil {
		return
	}
	if len(report.modelCAMBIKeys) == 0 {
		return false, nil
	}
	for _, key := range report.modelCAMBIKeys {
		if report.pooledCAMBIMax[key] <= 0 {
			err = fmt.Errorf("can not tell whether libvmaf clips the CAMBI of %s: its %s rates the banded probe pictures %v",
				config.Model, key, report.pooledCAMBIMax[key])
			return
		}
	}
	clipped := vmafPass{model: config.Model, modelCAMBI: true, measures: VMAFMeasures{Fidelity: true}}
	if report, err = runVMAFProbe(ctx, vmafProbeBandedPictures, clipped, config.ReportDir, config.Debug); err != nil {
		return
	}
	if len(report.modelCAMBIKeys) == 0 {
		err = fmt.Errorf("%w %s: no CAMBI feature reported once clipped", ErrVMAFCAMBIClip, config.Model)
		return
	}
	for _, key := range report.modelCAMBIKeys {
		if report.pooledCAMBIMax[key] != 0 {
			err = fmt.Errorf("%w %s: its %s still rates the banded probe pictures %v",
				ErrVMAFCAMBIClip, config.Model, key, report.pooledCAMBIMax[key])
			return
		}
	}
	return true, nil
}

// runVMAFProbe runs a pass on two frames of a lavfi source scored against themselves, and returns
// its report: see VMAFProbe.
func runVMAFProbe(ctx context.Context, pictures string, pass vmafPass, reportDir string, debug func(string)) (
	report VMAFReport, err error) {
	// Report file, unique in case of concurrent probes
	reportFd, err := os.CreateTemp(reportDir, vmafProbeFileMask)
	if err != nil {
		err = fmt.Errorf("failed to create the probe report file: %w", err)
		return
	}
	reportPath := reportFd.Name()
	reportFd.Close()
	defer os.Remove(reportPath)
	// One generated stream split in two: the distorted side is the reference itself
	args := []string{
		"-loglevel", "error", "-nostats", "-nostdin", "-y",
		"-f", "lavfi", "-i", pictures,
		"-filter_complex", "[0:v]format=yuv420p10le,split[distorted][reference];[distorted][reference]" + pass.filter(reportPath, 1),
		"-f", "null", "-",
	}
	if debug != nil {
		debug(fmt.Sprintf("Probe libvmaf with: %s", getPrintableCMDLine(FFMPEGBinary, args)))
	}
	cmd := exec.CommandContext(ctx, FFMPEGBinary, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	runErr := cmd.Run()
	output := strings.TrimSpace(stderr.String())
	if strings.Contains(output, vmafModelUnavailableMarker) {
		err = fmt.Errorf("%w: %s", ErrVMAFModelUnavailable, output)
		return
	}
	if runErr != nil {
		err = fmt.Errorf("error during %s execution: %w\n%s\n%s", FFMPEGBinary, runErr, output, getPrintableCMDLine(FFMPEGBinary, args))
		return
	}
	// Parse the report
	if report, err = readVMAFReport(reportPath, pass); err != nil {
		err = fmt.Errorf("libvmaf did not produce a usable report with model %s: %w\n%s", pass.model, err, output)
		return
	}
	if len(report.Frames) != vmafProbeFrames {
		err = fmt.Errorf("libvmaf scored %d frames out of %d with model %s\n%s", len(report.Frames), vmafProbeFrames, pass.model, output)
		return
	}
	return
}

/*
 * Compute
 */

// VMAFComputeConfig holds the parameters for a VMAF computation.
type VMAFComputeConfig struct {
	// Input
	ReferencePath  string // Path to the reference (original) video.
	DistortedPath  string // Path to the distorted (encoded) video.
	InputFrameRate string // Frame rate of the input videos (e.g. "24" or "24000/1001").
	// ReferenceRGBToYUV and DistortedRGBToYUV are the matrices an RGB input is converted with (see
	// YUVMatrix), empty for a YUV one: the encodes of an RGB source are scored against the very
	// pictures of its master. Left to ffmpeg, an RGB input was converted with the matrix the other
	// one declares, and with BT.601 when it declares none. An RGB input must be given one: once
	// both inputs declare the same matrix (see vmafSameMatrix), ffmpeg would convert it with
	// BT.601's.
	ReferenceRGBToYUV YUVMatrix
	DistortedRGBToYUV YUVMatrix
	// VMAF generation
	ReportPath string       // Path where the JSON VMAF report will be written.
	Model      VMAFModel    // Model to score with (see SelectVMAFModel).
	ModelCAMBI bool         // Whether the model feeds on CAMBI (see VMAFCAMBIProbe).
	Measures   VMAFMeasures // What the pass measures.
	// Hardware decode
	HWDecoderConfig
	// Reporting
	Debug             func(msg string)          // Optional debug logger.
	RuntimeError      func(err error)           // Optional callback for non-fatal runtime errors.
	FFMPEGStatsReport func(stats ProgressStats) // Optional callback for FFmpeg progress updates.
}

// VMAFCompute runs FFmpeg with libvmaf to compare a distorted video against its
// reference and returns the parsed VMAF report. The context can be used to cancel
// the long-running ffmpeg process.
func VMAFCompute(ctx context.Context, config VMAFComputeConfig) (stats VMAFReport, err error) {
	// Validate inputs
	if config.ReferencePath == "" {
		err = errors.New("reference path cannot be empty")
		return
	}
	if config.DistortedPath == "" {
		err = errors.New("distorted path cannot be empty")
		return
	}
	if config.ReportPath == "" {
		err = errors.New("report path cannot be empty")
		return
	}
	if config.InputFrameRate == "" {
		err = errors.New("input frame rate must be set")
		return
	}
	pass := vmafPass{model: config.Model, modelCAMBI: config.ModelCAMBI, measures: config.Measures}
	if err = pass.validate(); err != nil {
		return
	}
	if config.ReferenceRGBToYUV != "" && !config.ReferenceRGBToYUV.Valid() {
		err = fmt.Errorf("invalid matrix %q to convert the RGB reference to YUV with", config.ReferenceRGBToYUV)
		return
	}
	if config.DistortedRGBToYUV != "" && !config.DistortedRGBToYUV.Valid() {
		err = fmt.Errorf("invalid matrix %q to convert the RGB distorted video to YUV with", config.DistortedRGBToYUV)
		return
	}
	// Apply defaults
	if config.NVDevice == 0 {
		config.NVDevice = CUDADefaultDevice
	}
	if config.VAAPIDevice == "" {
		config.VAAPIDevice = VAAPIDefaultDevice
	}
	if config.D3D12Device == 0 {
		config.D3D12Device = D3D12VADefaultDevice
	}
	// Build up ffmpeg args
	args := []string{
		"-loglevel", "error", "-nostats", "-progress", "pipe:1", "-stats_period",
		strconv.FormatFloat(float64(StatsPeriod)/float64(time.Second), 'f', -1, 64),
	}
	// Hardware-accelerated decoding, when requested and when the codec allows it
	distortedHW := HWDecoderConfig{}
	referenceHW := HWDecoderConfig{}
	if config.NVDec || config.VAAPIDec || config.D3D12Dec || config.VideoToolboxDec {
		distortedHW = SelectCompatibleDecoders(ctx, config.DistortedPath,
			config.NVDec, config.VAAPIDec, config.D3D12Dec, config.VideoToolboxDec,
			config.NVDevice, config.VAAPIDevice, config.D3D12Device,
		)
		referenceHW = SelectCompatibleDecoders(ctx, config.ReferencePath,
			config.NVDec, config.VAAPIDec, config.D3D12Dec, config.VideoToolboxDec,
			config.NVDevice, config.VAAPIDevice, config.D3D12Device,
		)
	}
	//// distorted file first
	args = appendHWAccelArgs(args, distortedHW)
	args = append(args,
		"-r", config.InputFrameRate,
		"-i", config.DistortedPath,
	)
	//// ref file
	args = appendHWAccelArgs(args, referenceHW)
	args = append(args,
		"-r", config.InputFrameRate,
		"-i", config.ReferencePath,
	)
	//// vmaf filter
	inputFilters := func(rgbToYUV YUVMatrix) string {
		filters := "setpts=PTS-STARTPTS"
		if rgbToYUV != "" {
			filters += "," + RGBToYUVFilter(rgbToYUV)
		}
		return filters + "," + vmafSameMatrix
	}
	args = append(args,
		"-filter_complex",
		"[0:v]"+inputFilters(config.DistortedRGBToYUV)+"[distorted];[1:v]"+inputFilters(config.ReferenceRGBToYUV)+
			"[reference];[distorted][reference]"+pass.filter(config.ReportPath, NbThreadsToUse),
	)
	//// no ffmpeg output
	args = append(args, "-f", "null", "-")
	// Prepare command
	if config.Debug != nil {
		config.Debug(fmt.Sprintf("Compute VMAF with: %s", getPrintableCMDLine(FFMPEGBinary, args)))
	}
	cmd := exec.CommandContext(ctx, FFMPEGBinary, args...)
	//// Prepare output handling
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		err = fmt.Errorf("error setting up stdout pipe: %w", err)
		return
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		err = fmt.Errorf("error setting up stderr pipe: %w", err)
		return
	}
	// Start program
	if err = cmd.Start(); err != nil {
		err = fmt.Errorf("error starting %s: %w\n%s", FFMPEGBinary, err, getPrintableCMDLine(FFMPEGBinary, args))
		return
	}
	// Start progress monitoring (after cmd.Start to avoid goroutine leak on error)
	progressDone := make(chan struct{})
	go func() {
		defer close(progressDone)
		standardProgress(stdoutPipe, config.FFMPEGStatsReport, config.RuntimeError)
	}()
	stderrDone := make(chan struct{})
	go func() {
		stderrForwarder(stderrPipe, config.RuntimeError)
		close(stderrDone)
	}()
	if err = processpriority.Set(cmd.Process.Pid, ProcessPriority); err != nil && config.RuntimeError != nil {
		config.RuntimeError(fmt.Errorf("Failed to lower %s process priority: %w", FFMPEGBinary, err))
	}
	<-progressDone
	<-stderrDone
	if err = cmd.Wait(); err != nil {
		err = fmt.Errorf("error during %s execution: %w\n%s", FFMPEGBinary, err, getPrintableCMDLine(FFMPEGBinary, args))
		return
	}
	// Parse report
	return readVMAFReport(config.ReportPath, pass)
}

// vmafSameMatrix gives both inputs of libvmaf the same matrix, an unspecified one, for ffmpeg not
// to convert either into the matrix of the other: VMAF compares the pictures as they are decoded.
//
// # WHY
//
// The libvmaf filter takes its two inputs in one pixel format, one range and one matrix, and ffmpeg
// converts an input declaring another matrix than the other's (libavfilter negotiates the matrix
// since ffmpeg 7.0). Two files of the very same pictures, one declaring BT.709 and the other no
// matrix (or BT.601's), scored a harmonic mean of fidelity of 77.3: ffmpeg converted the BT.709 one
// into the other's matrix, BT.601's coefficients either way (swscale computes an unspecified matrix
// with them). Relabelled, they score 100.
// The matrix a YUV file declares is a label on its pictures: no step of sptenc converts them into
// another matrix (the encodes are made of the very pictures of the master), and an encode or a file
// declaring another matrix, or none, does not hold other pictures for it. Converting it would score
// a conversion sptenc never made.
//
// # WHY NOT THE RANGE
//
// ffmpeg still converts an input to the range of the other: a full range source is converted to
// limited range when its master is written, and the final VMAF scores its encodes against it once
// brought to their range. The scores of a full range 4:4:4 source against its encode are the same,
// frame by frame, with and without the relabelling.
//
// # EDGE CASES
//
//   - An RGB input is converted to YUV first (see VMAFComputeConfig.ReferenceRGBToYUV): an RGB
//     picture needs a matrix to become YUV, and relabelled, the matrix ffmpeg would pick is the
//     unspecified one, BT.601's coefficients.
const vmafSameMatrix = "setparams=colorspace=unknown"

// appendHWAccelArgs appends the appropriate -hwaccel flags for the given decoder config.
func appendHWAccelArgs(args []string, dec HWDecoderConfig) []string {
	if dec.NVDec {
		args = append(args, "-hwaccel", "cuda")
		if dec.NVDevice >= 0 {
			args = append(args, "-hwaccel_device", strconv.Itoa(dec.NVDevice))
		}
	} else if dec.VAAPIDec {
		args = append(args, "-hwaccel", "vaapi")
		if dec.VAAPIDevice != "" {
			args = append(args, "-vaapi_device", dec.VAAPIDevice)
		}
	} else if dec.D3D12Dec {
		args = append(args, "-hwaccel", "d3d12va")
		if dec.D3D12Device >= 0 {
			args = append(args, "-hwaccel_device", strconv.Itoa(dec.D3D12Device))
		}
	} else if dec.VideoToolboxDec {
		args = append(args, "-hwaccel", "videotoolbox")
	}
	return args
}

/*
 * Report
 */

// VMAFReport holds what sptenc reads from the report of a libvmaf pass (see readVMAFReport): per
// frame and pooled over the frames, the scores the pass computed and the banding measured in
// full-reference mode. What the report does not hold is zero, the Has fields tell.
type VMAFReport struct {
	Version     string
	FPS         float64
	Frames      []VMAFFrameMetrics // in the order of the report, the order of the frames
	Pooled      VMAFPooledMetrics
	HasFidelity bool
	HasOriginal bool
	HasBanding  bool
	// modelCAMBIKeys are the keys of the model's CAMBI features in the pooled metrics (see
	// modelCAMBIKeys), and pooledCAMBIMax their pooled maximums: VMAFCAMBIProbe decides on them.
	modelCAMBIKeys []string
	pooledCAMBIMax map[string]float64
}

// VMAFFrameMetrics holds the metrics of one frame sptenc reads out of the ones libvmaf reports.
type VMAFFrameMetrics struct {
	Fidelity float64 // See VMAFScoreFidelity.
	Original float64 // See VMAFScoreOriginal.
	Banding  VMAFBanding
}

// VMAFBanding is the banding CAMBI rates in full-reference mode (see bandingFeature), from 0
// (none) up: "a CAMBI score around 5 is where banding starts to become slightly annoying"
// (libvmaf's CAMBI documentation).
type VMAFBanding struct {
	Encode float64 // CAMBI of the distorted picture.
	Source float64 // CAMBI of the reference.
	Added  float64 // What the distorted picture adds: max(0, Encode - Source), frame by frame.
}

// VMAFPooledMetrics holds the metrics of VMAFFrameMetrics pooled over the frames by libvmaf.
type VMAFPooledMetrics struct {
	Fidelity VMAFPooledMetric
	Original VMAFPooledMetric
	Banding  VMAFPooledBanding
}

// VMAFPooledBanding holds the metrics of VMAFBanding pooled over the frames by libvmaf.
type VMAFPooledBanding struct {
	Encode VMAFPooledMetric
	Source VMAFPooledMetric
	Added  VMAFPooledMetric
}

// VMAFPooledMetric holds aggregate values (min, max, mean, harmonic mean) for a single metric.
type VMAFPooledMetric struct {
	Min          float64 `json:"min"`
	Max          float64 `json:"max"`
	Mean         float64 `json:"mean"`
	HarmonicMean float64 `json:"harmonic_mean"`
}

// readVMAFReport reads the report of a pass, on explicit keys: the scores under the keys the pass
// gives them (see vmafPass.scoreKeys), and the three keys of the banding feature (see
// bandingFeature). A key the pass asks for and the report does not hold, in any frame or in the
// pooled metrics, is an error, and so is a report without any frame.
//
// # WHY EXPLICIT KEYS
//
// libvmaf keys a feature by a name built from its options. A pass with the banding feature holds
// up to five CAMBI keys (the model's, twice when it is loaded clipped and as is, and the distorted
// picture's, the reference's and the added banding of the feature): a key picked by its "cambi"
// prefix is whichever sorts first. And libvmaf can leave out what was asked for while ffmpeg
// succeeds: the source's CAMBI and the added banding when the feature is taken for the model's
// (see bandingFeature), the whole feature when both sides of the pictures are under 216 pixels
// (see VMAFProbeConfig). A missing key must stop the run, not read as zero.
//
// # THE MODEL'S CAMBI
//
// It is not a metric sptenc reports: it rates the banding of the distorted picture, the source's
// included, and the banding feature measures what the distorted picture adds. Its keys are only
// looked for, in the pooled metrics, for VMAFCAMBIProbe to tell whether the model feeds on CAMBI.
// They are built from the options of the model (cambi_hrs_1080_cmxv_17_vlt_0.06 for the v1.0.16
// models, cmxv_0 under the fidelity clip), which a retrained model changes: sptenc does not choose
// them, and can only find them among the CAMBI keys as the ones that are not the banding feature's
// (see modelCAMBIKeys).
func readVMAFReport(path string, pass vmafPass) (report VMAFReport, err error) {
	reportFd, err := os.Open(path)
	if err != nil {
		err = fmt.Errorf("failed to open VMAF report file: %w", err)
		return
	}
	defer reportFd.Close()
	if report, err = decodeVMAFReport(bufio.NewReader(reportFd), pass); err != nil {
		err = fmt.Errorf("error parsing VMAF JSON output: %w", err)
	}
	return
}

// decodeVMAFReport decodes a libvmaf JSON report for a pass (see readVMAFReport), frame by frame:
// only the metrics sptenc reads are kept.
func decodeVMAFReport(r io.Reader, pass vmafPass) (report VMAFReport, err error) {
	var keys vmafReportKeys
	keys.fidelity, keys.original = pass.scoreKeys()
	keys.banding = pass.measures.Banding
	report.HasFidelity = keys.fidelity != ""
	report.HasOriginal = keys.original != ""
	report.HasBanding = keys.banding
	dec := json.NewDecoder(r)
	if err = expectJSONDelim(dec, '{'); err != nil {
		return
	}
	var pooled map[string]VMAFPooledMetric
	for dec.More() {
		var key string
		if key, err = readJSONKey(dec); err != nil {
			return
		}
		switch key {
		case "version":
			err = dec.Decode(&report.Version)
		case "fps":
			err = dec.Decode(&report.FPS)
		case "frames":
			err = report.decodeFrames(dec, keys)
		case "pooled_metrics":
			err = dec.Decode(&pooled)
		default:
			err = dec.Decode(new(json.RawMessage)) // not read
		}
		if err != nil {
			err = fmt.Errorf("%s: %w", key, err)
			return
		}
	}
	if err = expectJSONDelim(dec, '}'); err != nil {
		return
	}
	switch {
	case len(report.Frames) == 0:
		err = errors.New("no frame in the report")
	case pooled == nil:
		err = errors.New("no pooled metrics in the report")
	default:
		if err = report.readPooled(pooled, keys); err != nil {
			err = fmt.Errorf("pooled_metrics: %w", err)
		}
	}
	return
}

// vmafReportKeys are the keys a report is read on (see readVMAFReport): the report keys of the
// scores, empty when not computed, and whether the banding feature is in the pass.
type vmafReportKeys struct {
	fidelity, original string
	banding            bool
}

// decodeFrames decodes the frames of a report one by one, keeping the metrics sptenc reads.
func (vr *VMAFReport) decodeFrames(dec *json.Decoder, keys vmafReportKeys) (err error) {
	if err = expectJSONDelim(dec, '['); err != nil {
		return
	}
	for dec.More() {
		var frame struct {
			FrameNum int                `json:"frameNum"`
			Metrics  map[string]float64 `json:"metrics"`
		}
		if err = dec.Decode(&frame); err != nil {
			return
		}
		var metrics VMAFFrameMetrics
		if metrics.Fidelity, err = reportMetric(frame.Metrics, keys.fidelity); err != nil {
			return fmt.Errorf("frame %d: %w", frame.FrameNum, err)
		}
		if metrics.Original, err = reportMetric(frame.Metrics, keys.original); err != nil {
			return fmt.Errorf("frame %d: %w", frame.FrameNum, err)
		}
		if keys.banding {
			if metrics.Banding.Encode, err = reportMetric(frame.Metrics, bandingEncodeKey); err != nil {
				return fmt.Errorf("frame %d: %w", frame.FrameNum, err)
			}
			if metrics.Banding.Source, err = reportMetric(frame.Metrics, bandingSourceKey); err != nil {
				return fmt.Errorf("frame %d: %w", frame.FrameNum, err)
			}
			if metrics.Banding.Added, err = reportMetric(frame.Metrics, bandingAddedKey); err != nil {
				return fmt.Errorf("frame %d: %w", frame.FrameNum, err)
			}
		}
		vr.Frames = append(vr.Frames, metrics)
	}
	return expectJSONDelim(dec, ']')
}

// readPooled reads the pooled metrics of a report.
func (vr *VMAFReport) readPooled(pooled map[string]VMAFPooledMetric, keys vmafReportKeys) (err error) {
	if vr.Pooled.Fidelity, err = reportMetric(pooled, keys.fidelity); err != nil {
		return
	}
	if vr.Pooled.Original, err = reportMetric(pooled, keys.original); err != nil {
		return
	}
	if keys.banding {
		if vr.Pooled.Banding.Encode, err = reportMetric(pooled, bandingEncodeKey); err != nil {
			return
		}
		if vr.Pooled.Banding.Source, err = reportMetric(pooled, bandingSourceKey); err != nil {
			return
		}
		if vr.Pooled.Banding.Added, err = reportMetric(pooled, bandingAddedKey); err != nil {
			return
		}
	}
	vr.modelCAMBIKeys = modelCAMBIKeys(pooled)
	vr.pooledCAMBIMax = make(map[string]float64, len(vr.modelCAMBIKeys))
	for _, key := range vr.modelCAMBIKeys {
		vr.pooledCAMBIMax[key] = pooled[key].Max
	}
	return
}

// reportMetric returns the metric of a report under key, the zero value for an empty key (not
// asked for), an error when the report does not hold it.
func reportMetric[V any](metrics map[string]V, key string) (value V, err error) {
	if key == "" {
		return
	}
	value, found := metrics[key]
	if !found {
		err = fmt.Errorf("no %q metric", key)
	}
	return
}

// modelCAMBIKeys returns, sorted, the keys of the CAMBI features of a model among the metrics of a
// report: the keys starting with "cambi" that are not the banding feature's (see readVMAFReport).
func modelCAMBIKeys[V any](metrics map[string]V) (keys []string) {
	for key := range metrics {
		switch key {
		case bandingEncodeKey, bandingSourceKey, bandingAddedKey:
		default:
			if strings.HasPrefix(key, cambiMetricPrefix) {
				keys = append(keys, key)
			}
		}
	}
	sort.Strings(keys)
	return
}

// expectJSONDelim reads the next token of a JSON stream, which must be the delimiter delim.
func expectJSONDelim(dec *json.Decoder, delim json.Delim) error {
	token, err := dec.Token()
	if err != nil {
		return err
	}
	if token != delim {
		return fmt.Errorf("expected %q, got %v", delim, token)
	}
	return nil
}

// readJSONKey reads the next key of a JSON object from a stream.
func readJSONKey(dec *json.Decoder) (string, error) {
	token, err := dec.Token()
	if err != nil {
		return "", err
	}
	key, ok := token.(string)
	if !ok {
		return "", fmt.Errorf("expected an object key, got %v", token)
	}
	return key, nil
}

// VMAF percentile constants for statistics calculation.
const (
	vmafPercentile1  = 1
	vmafPercentile5  = 5
	vmafPercentile10 = 10
	vmafPercentile25 = 25
	vmafPercentile50 = 50 // Median
)

// Stats returns the statistics of a score over the frames: the pooled ones of libvmaf, and the
// percentiles libvmaf does not compute. A score the report does not hold is an error.
func (vr VMAFReport) Stats(score VMAFScore) (vs VMAFStats, err error) {
	var (
		pooled VMAFPooledMetric
		value  func(VMAFFrameMetrics) float64
	)
	switch {
	case score == VMAFScoreFidelity && vr.HasFidelity:
		pooled, value = vr.Pooled.Fidelity, func(m VMAFFrameMetrics) float64 { return m.Fidelity }
	case score == VMAFScoreOriginal && vr.HasOriginal:
		pooled, value = vr.Pooled.Original, func(m VMAFFrameMetrics) float64 { return m.Original }
	default:
		err = fmt.Errorf("the VMAF report holds no %s score", score)
		return
	}
	vs.Version = vr.Version
	// Copy existing metrics
	vs.Minimum = pooled.Min
	vs.HarmonicMean = pooled.HarmonicMean
	vs.Mean = pooled.Mean
	vs.Maximum = pooled.Max
	// Compute the missing ones
	scores := make([]float64, len(vr.Frames))
	for i, frame := range vr.Frames {
		scores[i] = value(frame)
	}
	sort.Float64s(scores)
	vs.Percentile1 = scorePercentile(scores, vmafPercentile1)
	vs.Percentile5 = scorePercentile(scores, vmafPercentile5)
	vs.Percentile10 = scorePercentile(scores, vmafPercentile10)
	vs.Percentile25 = scorePercentile(scores, vmafPercentile25)
	vs.Median = scorePercentile(scores, vmafPercentile50)
	return
}

// scorePercentile returns the score at the given percentile of sorted scores.
func scorePercentile(sorted []float64, p float64) float64 {
	index := int(math.Round(float64(len(sorted))/100*p)) - 1
	if index == -1 {
		index = 0
	}
	if index < 0 || index >= len(sorted) {
		return 0
	}
	return sorted[index]
}

// VMAFStats is a user-friendly summary of the statistics of a VMAF score over the frames,
// including computed percentiles.
type VMAFStats struct {
	Version      string
	Minimum      float64 `json:"min"`
	Percentile1  float64 `json:"p1"`
	Percentile5  float64 `json:"p5"`
	Percentile10 float64 `json:"p10"`
	Percentile25 float64 `json:"p25"`
	Median       float64 `json:"median"`
	HarmonicMean float64 `json:"harmonic_mean"`
	Mean         float64 `json:"mean"`
	Maximum      float64 `json:"max"`
}

// String renders the VMAF statistics as an aligned plain-text table.
func (vs VMAFStats) String() string {
	var tableBuffer strings.Builder
	table := tablewriter.NewTable(&tableBuffer,
		tablewriter.WithRenderer(renderer.NewBlueprint(tw.Rendition{
			Borders: tw.BorderNone,
			Symbols: tw.NewSymbolCustom("box").
				WithRow("─").
				WithColumn("│").
				WithCenter("┼"),
		})),
		tablewriter.WithConfig(tablewriter.Config{
			Header: tw.CellConfig{
				Formatting: tw.CellFormatting{
					AutoFormat: tw.Off,
				},
			},
			Row: tw.CellConfig{
				Alignment: tw.CellAlignment{Global: tw.AlignCenter},
			},
		}),
	)
	table.Header([]string{
		"Min",
		"P1",
		"P5",
		"P10",
		"P25",
		"Median",
		"Harmonic Mean",
		"Mean",
		"Max",
	})
	table.Append([]string{
		strconv.FormatFloat(vs.Minimum, 'f', -1, float64Precision),
		strconv.FormatFloat(vs.Percentile1, 'f', -1, float64Precision),
		strconv.FormatFloat(vs.Percentile5, 'f', -1, float64Precision),
		strconv.FormatFloat(vs.Percentile10, 'f', -1, float64Precision),
		strconv.FormatFloat(vs.Percentile25, 'f', -1, float64Precision),
		strconv.FormatFloat(vs.Median, 'f', -1, float64Precision),
		strconv.FormatFloat(vs.HarmonicMean, 'f', -1, float64Precision),
		strconv.FormatFloat(vs.Mean, 'f', -1, float64Precision),
		strconv.FormatFloat(vs.Maximum, 'f', -1, float64Precision),
	})
	table.Render()
	return tableBuffer.String()
}

// VMAFSummary is what sptenc reports of a pass over a whole video, the final VMAF of an encode or
// the vmaf command: the statistics of the score gated, the harmonic mean of the model's other
// score, and the banding the distorted video adds to its reference, as far as the pass measured
// them (see VMAFReport.Summary).
type VMAFSummary struct {
	// Score is the score of Stats, empty for a model without CAMBI: its single score is both, and
	// is not named (see VMAFScore).
	Score VMAFScore
	Stats VMAFStats
	// OtherScore is the other score of a model fed with CAMBI, empty when the pass did not compute
	// it, and OtherHarmonicMean its harmonic mean over the frames.
	OtherScore        VMAFScore
	OtherHarmonicMean float64
	HasBanding        bool
	Banding           VMAFBandingSummary
}

// VMAFBandingSummary is the banding a distorted video adds to its reference, as CAMBI rates it in
// full-reference mode (see VMAFBanding), pooled over the frames.
type VMAFBandingSummary struct {
	AddedMean  float64 // The banding added, averaged over the frames.
	AddedMax   float64 // The banding added to the worst frame.
	SourceMean float64 // The banding of the reference, averaged over the frames.
	EncodeMean float64 // The banding of the distorted video, averaged over the frames.
}

// Summary returns the summary of the report for the score gated (see VMAFSummary). modelCAMBI tells
// whether the model feeds on CAMBI (see VMAFCAMBIProbe): without it, its single score is both
// scores of the report, and names neither.
func (vr VMAFReport) Summary(score VMAFScore, modelCAMBI bool) (summary VMAFSummary, err error) {
	if summary.Stats, err = vr.Stats(score); err != nil {
		return
	}
	if modelCAMBI {
		summary.Score = score
		switch {
		case score == VMAFScoreFidelity && vr.HasOriginal:
			summary.OtherScore, summary.OtherHarmonicMean = VMAFScoreOriginal, vr.Pooled.Original.HarmonicMean
		case score == VMAFScoreOriginal && vr.HasFidelity:
			summary.OtherScore, summary.OtherHarmonicMean = VMAFScoreFidelity, vr.Pooled.Fidelity.HarmonicMean
		}
	}
	if summary.HasBanding = vr.HasBanding; summary.HasBanding {
		summary.Banding = VMAFBandingSummary{
			AddedMean:  vr.Pooled.Banding.Added.Mean,
			AddedMax:   vr.Pooled.Banding.Added.Max,
			SourceMean: vr.Pooled.Banding.Source.Mean,
			EncodeMean: vr.Pooled.Banding.Encode.Mean,
		}
	}
	return
}

// vmafScoreTitles are the names the scores are printed under.
var vmafScoreTitles = map[VMAFScore]string{
	VMAFScoreFidelity: "Fidelity",
	VMAFScoreOriginal: "Original score",
}

// String renders the summary: the table of the statistics of the score, under its name when the
// model has two, the harmonic mean of the other one, and the banding.
func (vs VMAFSummary) String() string {
	var buffer strings.Builder
	if vs.Score != "" {
		fmt.Fprintf(&buffer, "%s:\n", vmafScoreTitles[vs.Score])
	}
	buffer.WriteString(vs.Stats.String())
	if vs.OtherScore != "" {
		fmt.Fprintf(&buffer, "%s: harmonic mean %s\n", vmafScoreTitles[vs.OtherScore],
			strconv.FormatFloat(vs.OtherHarmonicMean, 'f', -1, float64Precision))
	}
	if vs.HasBanding {
		// Two kinds of values on CAMBI's scale, the banding of a picture, one line each. First how each video rates,
		// next to the legend of the scale, which describes ratings. Then the banding added the gate reads, how much
		// higher the distorted video rates than the reference, signed as the rises they are: unsigned, or next to
		// the legend, they read as ratings. Computed frame by frame, 0 where the distorted video does not rate
		// higher, the rises are not the difference of the two ratings above them.
		fmt.Fprintf(&buffer, "Banding on CAMBI's scale (0 = no banding, ~5 = slightly annoying): the reference rates %s on average over the frames, the distorted video %s.\n",
			strconv.FormatFloat(vs.Banding.SourceMean, 'f', -1, float64Precision),
			strconv.FormatFloat(vs.Banding.EncodeMean, 'f', -1, float64Precision),
		)
		fmt.Fprintf(&buffer, "Banding added, frame by frame how much higher the distorted video rates than the reference: +%s on average over the frames, +%s on the worst frame.\n",
			strconv.FormatFloat(vs.Banding.AddedMean, 'f', -1, float64Precision),
			strconv.FormatFloat(vs.Banding.AddedMax, 'f', -1, float64Precision),
		)
	}
	return buffer.String()
}

// vmafPathEscaper applies the two levels of escaping needed by a filter option value
// embedded in a filtergraph description (without any quoting):
//   - 1st level, the filter option value: \ ' and : are special
//   - 2nd level, the filtergraph description: \ ' [ ] , and ; are special
//
// Each replacement below is the 1st level escaping of the character, escaped again for the 2nd level.
// https://ffmpeg.org/ffmpeg-filters.html#Notes-on-filtergraph-escaping
//
// The same escaping works on every platform: Windows paths do not need their backslashes
// converted, only escaped (checked against a Windows ffmpeg build, drive colon included).
var vmafPathEscaper = strings.NewReplacer(
	`\`, `\\\\`,
	`'`, `\\\'`,
	`:`, `\\:`,
	`[`, `\[`,
	`]`, `\]`,
	`,`, `\,`,
	`;`, `\;`,
)

// adaptVMAFPath escapes a file path to be used as the libvmaf log_path within a filtergraph description.
func adaptVMAFPath(path string) string {
	return vmafPathEscaper.Replace(path)
}
