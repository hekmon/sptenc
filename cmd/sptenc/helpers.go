package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"al.essio.dev/pkg/shellescape"
	"github.com/hekmon/sptenc/core"
	"github.com/hekmon/sptenc/ffmpeg"
	"github.com/hekmon/sptenc/mkvtoolnix"
	"github.com/hekmon/sptenc/pipeline"

	"github.com/hekmon/liveprogress/v2"
	"github.com/urfave/cli/v3"
)

func checkFFMPEG(ctx context.Context) error {
	if _, err := ffmpeg.GetFFMPEGVersion(ctx); err != nil {
		return fmt.Errorf("ffmpeg check failed: %w", err)
	}
	return nil
}

func checkFFProbe(ctx context.Context) error {
	if _, err := ffmpeg.GetFFProbeVersion(ctx); err != nil {
		return fmt.Errorf("ffprobe check failed: %w", err)
	}
	return nil
}

func checkMKVPropEdit(ctx context.Context) error {
	if _, err := mkvtoolnix.GetMKVPropEditVersion(ctx); err != nil {
		return fmt.Errorf("mkvpropedit check failed: %w", err)
	}
	return nil
}

// checkSourceVideo returns the video stream sptenc is going to work on, or the reason why it
// can not: no video stream at all, a variable frame rate or interlaced content.
//
// # WHY EVERY COMMAND READING A SOURCE GOES THROUGH IT, NOT ONLY THE ENCODING ONES
//
// master and split do not compute any VMAF nor encode anything, and did not check anything:
// they produce what encode takes as input. A master or a set of segments made out of an
// unsupported source was only rejected once given to encode, after the time and the disk space
// (a lossless master is large) had been spent. The master keeps the properties of its source
// (checked: an interlaced source gives a master declared as interlaced), so the verdict is the
// same at both ends: better to give it first.
func checkSourceVideo(stats ffmpeg.FFProbeStats) (videoStream *ffmpeg.FFProbeBinaryStream, err error) {
	if videoStream = stats.VideoTrack(); videoStream == nil {
		return nil, errors.New("no video stream found in source")
	}
	if !videoStream.IsConstantFrameRate() {
		return nil, errors.New("variable frame rate (VFR) content is not supported: VMAF requires CFR for frame-exact alignment")
	}
	if err = checkProgressive(videoStream); err != nil {
		return nil, err
	}
	return
}

// resolveVMAFModel returns the VMAF model of a run and tells the user which one and why: the
// one forced with the model flag, warned about when it is made for another display than the
// one of the source resolution (it is the point of forcing it: a 1440p source judged as 4K, 4K
// content meant for 1080p screens), said unknown when sptenc does not know it (a model newer
// than sptenc), and warned about when it is a v0 one (see ffmpeg.VMAFV0Models); the one of the
// source resolution otherwise (see ffmpeg.SelectVMAFModel).
func resolveVMAFModel(cmd *cli.Command, out io.Writer, stream *ffmpeg.FFProbeBinaryStream) ffmpeg.VMAFModel {
	if forced := cmd.String(vmafModelFlagName); forced != "" {
		model := ffmpeg.VMAFModel(forced)
		switch mismatch := model.ResolutionMismatch(stream.Height); {
		case mismatch != "":
			fmt.Fprintf(out, "WARNING: VMAF model %s forced by --%s: %s\n", model, vmafModelFlagName, mismatch)
		case model.Description() == "":
			fmt.Fprintf(out, "VMAF model %s forced by --%s: sptenc does not know this model, its viewing condition and its scale are yours to check\n",
				model, vmafModelFlagName)
		default:
			fmt.Fprintf(out, "VMAF model %s forced by --%s (%s)\n", model, vmafModelFlagName, model.Description())
		}
		if model.IsV0() {
			fmt.Fprintf(out, "WARNING: %s is a VMAF v0 model: it measures luma only and does not see banding (see MANUAL.md, What VMAF sees)\n", model)
		}
		return model
	}
	model := ffmpeg.SelectVMAFModel(stream.Height)
	fmt.Fprintf(out, "VMAF model %s selected for the %dp source (%s), use --%s to force another one\n",
		model, stream.Height, model.Description(), vmafModelFlagName)
	return model
}

// checkLibVMAF verifies before anything starts that the ffmpeg build can score with VMAF: the
// libvmaf filter is there and it knows the model of the run (the one forced with the model
// flag, the 1080p one otherwise: the v1 models come together, one of them is enough to tell
// the libvmaf version). A model libvmaf does not know surfaces mid-run otherwise, once the
// master and the first segment have been produced. The probe runs at 1080p, the size the
// models are made for: the size of the source is checked once it is known (see
// checkVMAFPictures). The probe report goes to the temporary directory of the command.
func checkLibVMAF(ctx context.Context, cmd *cli.Command) error {
	filters, err := ffmpeg.GetFilters(ctx)
	if err != nil {
		return fmt.Errorf("failed to list ffmpeg filters: %w", err)
	}
	if !filters.HasLibVMAF() {
		return errors.New("libvmaf is not available in this ffmpeg build; run 'sptenc check' to see available filters")
	}
	model := ffmpeg.VMAFModelFHD
	forced := cmd.String(vmafModelFlagName)
	if forced != "" {
		model = ffmpeg.VMAFModel(forced)
	}
	if _, err = ffmpeg.VMAFProbe(ctx, ffmpeg.VMAFProbeConfig{
		Model:     model,
		Measures:  ffmpeg.VMAFMeasures{Original: true},
		ReportDir: cmd.String(tmpDirFlagName),
	}); err != nil {
		switch {
		case errors.Is(err, ffmpeg.ErrVMAFModelUnavailable) && forced != "":
			return fmt.Errorf("the libvmaf of this ffmpeg build does not know the VMAF model %s forced by --%s: check its name, or the version of libvmaf (run 'sptenc check' for details)",
				model, vmafModelFlagName)
		case errors.Is(err, ffmpeg.ErrVMAFModelUnavailable):
			return fmt.Errorf("the libvmaf of this ffmpeg build does not know the VMAF model %s: sptenc needs ffmpeg built against libvmaf 3.2.0 or newer (run 'sptenc check' for details)", model)
		default:
			return fmt.Errorf("libvmaf check failed: %w", err)
		}
	}
	return nil
}

// vmafSetup is how a run scores the encodes of its segments, decided once its VMAF model is known
// (see setupVMAF).
type vmafSetup struct {
	model      ffmpeg.VMAFModel
	modelCAMBI bool              // the model feeds on CAMBI (see ffmpeg.VMAFCAMBIProbe)
	score      ffmpeg.VMAFScore  // the score the VMAF thresholds gate
	cambi      core.CAMBIChecker // the CAMBI gate, off (its zero value) for a command that gates nothing
}

// scoreMeasures returns the measures of a pass computing the gated score, and the banding the
// encode added when banding is set.
func (vs vmafSetup) scoreMeasures(banding bool) ffmpeg.VMAFMeasures {
	return ffmpeg.VMAFMeasures{
		Fidelity: vs.score == ffmpeg.VMAFScoreFidelity,
		Original: vs.score == ffmpeg.VMAFScoreOriginal,
		Banding:  banding,
	}
}

// passes returns every kind of libvmaf pass the run makes (see checkVMAFPictures): the gated score
// (the VMAF search, the final score), and with the CAMBI gate on, the banding alone (at the QP of
// the VMAF search) and both at once (the walk below it, see core.QPSearchConfig.CAMBIAuditor).
func (vs vmafSetup) passes() []ffmpeg.VMAFMeasures {
	passes := []ffmpeg.VMAFMeasures{vs.scoreMeasures(false)}
	if vs.cambi.Enabled() {
		passes = append(passes, ffmpeg.VMAFMeasures{Banding: true}, vs.scoreMeasures(true))
	}
	return passes
}

// setupVMAF decides how a run scores the encodes of its segments, now that its VMAF model is
// known, and says so: whether the model feeds on CAMBI, probed for real (see
// ffmpeg.VMAFCAMBIProbe), the score the VMAF thresholds gate, and for a command that gates (gate
// set: encode, batchsearch) the CAMBI gate (see resolveCAMBIGate). It then checks that libvmaf runs
// every pass of the run on pictures of the source's size (see checkVMAFPictures), before any long
// step.
//
// # WHY FIDELITY, ALWAYS
//
// The thresholds gate what the encoder did, and a model fed with CAMBI also scores the banding
// the source already has (see ffmpeg.VMAFScore): fidelity takes it out. A model without CAMBI has
// a single score, which is its fidelity score: nothing to take out, nothing to say.
func setupVMAF(ctx context.Context, cmd *cli.Command, out io.Writer, model ffmpeg.VMAFModel,
	stream *ffmpeg.FFProbeBinaryStream, gate bool) (setup vmafSetup, err error) {
	setup.model = model
	if setup.modelCAMBI, err = ffmpeg.VMAFCAMBIProbe(ctx, ffmpeg.VMAFCAMBIProbeConfig{
		Model:     model,
		ReportDir: cmd.String(tmpDirFlagName),
	}); err != nil {
		err = fmt.Errorf("failed to tell whether the VMAF model %s feeds on CAMBI: %w", model, err)
		return
	}
	setup.score = ffmpeg.VMAFScoreFidelity
	if setup.modelCAMBI {
		lead := "Scoring fidelity"
		if gate {
			lead = "The VMAF thresholds gate fidelity"
		}
		fmt.Fprintf(out, "%s: the score of %s with its banding feature (CAMBI) set to zero, 100 for the source against itself (see MANUAL.md, Fidelity and banding)\n",
			lead, model)
	}
	if gate {
		if setup.cambi, err = resolveCAMBIGate(cmd, setup.modelCAMBI); err != nil {
			return
		}
		if setup.cambi.Enabled() {
			fmt.Fprintf(out, "The banding the encoder adds is gated too (CAMBI, 0 = none, ~5 = slightly annoying): %s\n",
				describeCAMBIGate(setup.cambi))
		}
	}
	err = checkVMAFPictures(ctx, cmd, setup, stream)
	return
}

// resolveCAMBIGate returns the CAMBI gate of a run out of the CAMBI flags: on by default with a
// model feeding on CAMBI, off with both thresholds at -1.
//
// # WHY OFF BY DEFAULT WITH A MODEL WITHOUT CAMBI
//
// sptenc selects models feeding on CAMBI. A model without it (a v0 one) is forced to score as the
// published VMAF anchors were measured, or as sptenc v0.1.0 did: its default is v0.1.0's, no
// banding measured, printed, tagged nor cached. A CAMBI threshold turns the gate on for any model,
// the banding feature needing nothing from the model: then it is the gate with its thresholds, the
// defaults included.
func resolveCAMBIGate(cmd *cli.Command, modelCAMBI bool) (gate core.CAMBIChecker, err error) {
	mean, max := cmd.Float64(cambiMeanFlagName), cmd.Float64(cambiMaxFlagName)
	if !modelCAMBI && !(cmd.IsSet(cambiMeanFlagName) && mean != core.CAMBIOffValue) &&
		!(cmd.IsSet(cambiMaxFlagName) && max != core.CAMBIOffValue) {
		return // no threshold asked for: the gate stays off
	}
	if gate, err = core.NewCAMBIChecker(mean, max); err != nil {
		err = fmt.Errorf("invalid CAMBI thresholds: %w", err)
	}
	return
}

// describeCAMBIGate returns the active thresholds of a CAMBI gate, in words.
func describeCAMBIGate(gate core.CAMBIChecker) string {
	thresholds := gate.Thresholds()
	mean, hasMean := thresholds["mean"]
	max, hasMax := thresholds["max"]
	switch {
	case hasMean && hasMax:
		return fmt.Sprintf("at most %s on average over the frames of a segment, and %s on its worst frame",
			strconv.FormatFloat(mean, 'f', -1, 64), strconv.FormatFloat(max, 'f', -1, 64))
	case hasMean:
		return fmt.Sprintf("at most %s on average over the frames of a segment", strconv.FormatFloat(mean, 'f', -1, 64))
	case hasMax:
		return fmt.Sprintf("at most %s on the worst frame of a segment", strconv.FormatFloat(max, 'f', -1, 64))
	default:
		return "off"
	}
}

// checkVMAFPictures verifies, once the source is known and before any long step, that libvmaf
// runs every kind of pass of the run on pictures of its size (see vmafSetup.passes). Below a
// minimum size libvmaf crashes, or writes no report, or one without some metrics, and that
// minimum depends on the model, on the aspect ratio and on the pass (see ffmpeg.VMAFProbeConfig):
// two frames of that very size go through each pass for real rather than being held against a
// fixed floor. Refused later, the source would fail at its first VMAF, or at its first banding
// measure, after the master and a first encode.
func checkVMAFPictures(ctx context.Context, cmd *cli.Command, setup vmafSetup, stream *ffmpeg.FFProbeBinaryStream) error {
	for _, measures := range setup.passes() {
		if _, err := ffmpeg.VMAFProbe(ctx, ffmpeg.VMAFProbeConfig{
			Model:      setup.model,
			ModelCAMBI: setup.modelCAMBI,
			Measures:   measures,
			Width:      stream.Width,
			Height:     stream.Height,
			ReportDir:  cmd.String(tmpDirFlagName),
		}); err != nil {
			if measures.Banding {
				return fmt.Errorf("libvmaf can not measure the banding added to %dx%d pictures: %w\nThe CAMBI gate can be turned off with --%s %d --%s %d",
					stream.Width, stream.Height, err, cambiMeanFlagName, core.CAMBIOffValue, cambiMaxFlagName, core.CAMBIOffValue)
			}
			return fmt.Errorf("libvmaf can not score %dx%d pictures with the VMAF model %s: %w", stream.Width, stream.Height, setup.model, err)
		}
	}
	return nil
}

// checkProgressive rejects a video stream declared as interlaced.
//
// # WHY INTERLACED CONTENT IS REJECTED AND NOT DEINTERLACED
//
// Nothing in the pipeline knows about fields: frames are decoded with both fields woven
// together, stored that way in the master and encoded as progressive pictures. It works, no
// error, frame counts match and VMAF validates it (combed pictures compared with the same
// combed pictures). But the result is a stream of progressive frames holding combed pictures,
// with a container still declaring it interlaced (checked on the output of an interlaced H.264
// source: field order kept by the container, every decoded frame flagged as progressive).
// A deinterlacer trusting the frames will leave it alone: the combing is there to stay.
//
// Deinterlacing here is not an option either: which method to use (and whether to keep the
// field rate, doubling the frame rate) is a choice on the content itself, to be made once
// and before sptenc, not an encoding option.
func checkProgressive(stream *ffmpeg.FFProbeBinaryStream) error {
	if stream.IsInterlaced() {
		return fmt.Errorf("interlaced content is not supported (field order: %s): it would be encoded as progressive frames with the combing baked in. "+
			"Deinterlace it first (if the pictures are actually progressive within an interlaced stream, it must be encoded again as progressive as well)",
			stream.FieldOrder)
	}
	return nil
}

// isASCII checks if a string contains only ASCII characters (code points 0-127)
func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] > 127 {
			return false
		}
	}
	return true
}

func createTempDir(basePath string) (path string, err error) {
	return os.MkdirTemp(basePath, "sptenc-*")
}

func getCacheDir() string {
	userCacheDir, err := os.UserCacheDir()
	if err != nil {
		userCacheDir = os.TempDir()
	}
	return filepath.Join(userCacheDir, "sptenc")
}

// getSegmentsFromDir returns sorted list of video file paths from inputDir.
// Files are sorted alphabetically and returned as full paths.
func getSegmentsFromDir(inputDir string) (filePaths []string, err error) {
	entries, err := os.ReadDir(inputDir)
	if err != nil {
		err = fmt.Errorf("failed to read input directory: %w", err)
		return
	}
	// Filter video files
	var files []os.DirEntry
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(entry.Name()))
		if ext == ".mkv" || ext == ".mp4" {
			files = append(files, entry)
		}
	}
	// Sort by name to ensure consistent ordering
	sort.Slice(files, func(i, j int) bool {
		return files[i].Name() < files[j].Name()
	})
	// Build full paths
	filePaths = make([]string, len(files))
	for i, file := range files {
		filePaths[i] = filepath.Join(inputDir, file.Name())
	}
	return
}

// getSegmentsTotalDuration calculates the total duration of all segment files
func getSegmentsTotalDuration(ctx context.Context, segmentPaths []string, debug bool) (totalDuration time.Duration, err error) {
	var stats ffmpeg.FFProbeStats
	for _, path := range segmentPaths {
		if stats, err = getStreamsInfos(ctx, path, debug); err != nil {
			err = fmt.Errorf("failed to get stream info for segment %s: %w", path, err)
			return
		}
		totalDuration += stats.Format.Duration
	}
	return
}

func getFileSize(path string) (size int64, err error) {
	info, err := os.Stat(path)
	if err != nil {
		err = fmt.Errorf("failed to stat path: %w", err)
		return
	}
	size = info.Size()
	return
}

// validateOutputPath checks that the output path has a .mkv extension, does not
// already exist and that its parent directory exists and is writable. The output
// file is only written at the very end of what can be a multi-day process, so a
// bad destination must be caught upfront.
func validateOutputPath(outputPath string) error {
	if filepath.Ext(outputPath) != ".mkv" {
		return errors.New("output file must have a .mkv extension")
	}
	if _, err := os.Stat(outputPath); err == nil {
		return fmt.Errorf("output file already exists: %s", shellescape.Quote(outputPath))
	}
	outputDir := filepath.Dir(outputPath)
	dirInfos, err := os.Stat(outputDir)
	if err != nil {
		return fmt.Errorf("output directory is not accessible: %w", err)
	}
	if !dirInfos.IsDir() {
		return fmt.Errorf("output file parent is not a directory: %s", shellescape.Quote(outputDir))
	}
	// Permission bits do not tell the whole story (ACLs, read-only mounts, Windows...):
	// actually try to create a file.
	probe, err := os.CreateTemp(outputDir, ".sptenc-write-test-*")
	if err != nil {
		return fmt.Errorf("output directory is not writable: %w", err)
	}
	probe.Close()
	if err = os.Remove(probe.Name()); err != nil {
		return fmt.Errorf("failed to remove the write test file: %w", err)
	}
	return nil
}

// hasAudioTracks returns true if there is at least one audio stream in the given stats.
func hasAudioTracks(stats ffmpeg.FFProbeStats) bool {
	for _, stream := range stats.Streams {
		if stream.CodecType == "audio" {
			return true
		}
	}
	return false
}

// AllAudioTracksPCM returns true if there is at least one audio stream in the given stats and
// all of them are PCM encoded. A file without any audio is not "all PCM": there is nothing to
// compress to FLAC, and saying otherwise to the user is misleading.
func AllAudioTracksPCM(stats ffmpeg.FFProbeStats) bool {
	var audioTracks int
	for _, stream := range stats.Streams {
		if stream.CodecType == "audio" {
			if stream.CodecName != ffmpeg.CodecAudioPCM && stream.CodecName != ffmpeg.CodecAudioPCM24b {
				return false
			}
			audioTracks++
		}
	}
	return audioTracks > 0
}

// formatPercent formats a float64 as a percentage string with up to 1 decimal
// place, trimming trailing ".0" for whole numbers.
func formatPercent(v float64) string {
	s := strconv.FormatFloat(v, 'f', 1, 64)
	if strings.HasSuffix(s, ".0") {
		return s[:len(s)-2] + "%"
	}
	return s + "%"
}

// verifyColorMetadata probes the output file and warns via liveprogress.Bypass if its
// container-level color metadata is not what ffmpeg.RemuxSwapVideo writes: the color range of the
// encoded video when it declares one (see ffmpeg.RemuxColorRange), the values of the source video
// stream for the rest.
func verifyColorMetadata(ctx context.Context, outputPath string, sourceStream *ffmpeg.FFProbeBinaryStream, encodedPath string, debug bool) {
	bypass := liveprogress.Bypass()
	outputStats, err := getStreamsInfos(ctx, outputPath, debug)
	if err != nil {
		fmt.Fprintf(bypass, "WARNING: could not verify output color metadata: %s\n", err)
		return
	}
	outStream := outputStats.VideoTrack()
	if outStream == nil {
		fmt.Fprintf(bypass, "WARNING: output file has no video stream, can not verify color metadata\n")
		return
	}
	var encodedStream *ffmpeg.FFProbeBinaryStream
	if encodedStats, err := getStreamsInfos(ctx, encodedPath, debug); err == nil {
		encodedStream = encodedStats.VideoTrack()
	} else {
		fmt.Fprintf(bypass, "WARNING: could not probe the encoded video for its color range: %s\n", err)
	}
	if expectedRange := ffmpeg.RemuxColorRange(sourceStream, encodedStream); expectedRange != "" && outStream.ColorRange != expectedRange {
		origin := "encoded video"
		if expectedRange == sourceStream.ColorRange {
			origin = "source" // same value, or the encoded video declares none
		}
		fmt.Fprintf(bypass, "WARNING: output color_range (%s) does not match the %s (%s)\n", outStream.ColorRange, origin, expectedRange)
	}
	if sourceStream.ColorSpace != "" && outStream.ColorSpace != sourceStream.ColorSpace {
		fmt.Fprintf(bypass, "WARNING: output colorspace (%s) does not match source (%s)\n", outStream.ColorSpace, sourceStream.ColorSpace)
	}
	if sourceStream.ColorTransfer != "" && outStream.ColorTransfer != sourceStream.ColorTransfer {
		fmt.Fprintf(bypass, "WARNING: output color_trc (%s) does not match source (%s)\n", outStream.ColorTransfer, sourceStream.ColorTransfer)
	}
	if sourceStream.ColorPrimaries != "" && outStream.ColorPrimaries != sourceStream.ColorPrimaries {
		fmt.Fprintf(bypass, "WARNING: output color_primaries (%s) does not match source (%s)\n", outStream.ColorPrimaries, sourceStream.ColorPrimaries)
	}
}

// searchCandidates turns detected scenes into the threshold candidates of a search (batchsearch,
// thresholds) along with the scenes each one produces: segmentations[i] are the scenes of thresholds[i].
// The reasoning behind each step lives in core/scenes.go ("Threshold candidates"), in short:
//
//  1. scenes have been detected with --min-threshold (done by the caller)
//  2. candidates are capped to --max-threshold
//  3. raw candidates are the unique scene scores in that range
//  4. the scenes of each raw candidate are computed from the full scenes list: threshold first,
//     then --min-segment-length, exactly what encode does with its own threshold
//  5. candidates ending up with the same scenes are dropped
//  6. last, candidates are thinned out by scene drop (counted on real scenes, once merged) to fit
//     --max-candidates, without going below --min-drop
//
// Returned thresholds are not the raw scene scores: they are made reusable with encode -T
// (see pipeline.ReusableThreshold).
//
// No candidates are returned if there is no scene within the range.
func searchCandidates(out io.Writer, cmd *cli.Command, scenes []ffmpeg.Scene, totalDuration time.Duration) (
	thresholds []float64, segmentations [][]ffmpeg.Scene) {
	// Steps 2 to 5
	allCandidates := core.GetCandidates(pipeline.ToCoreScenes(scenes), cmd.Float64(maxThresholdFlagName),
		totalDuration, cmd.Duration(minSegmentLengthFlagName))
	if len(allCandidates) == 0 {
		return
	}
	fmt.Fprintf(out, "\tFound %d distinct candidates up to threshold %s (min segment length of %s enforced on each)\n",
		len(allCandidates), strconv.FormatFloat(cmd.Float64(maxThresholdFlagName), 'f', -1, 64),
		cmd.Duration(minSegmentLengthFlagName),
	)
	// Step 6
	candidates, effectiveMinDrop := core.GetOptimalMinDrop(allCandidates, cmd.Int(maxCandidatesFlagName))
	if effectiveMinDrop < cmd.Int(minDropFlagName) {
		fmt.Fprintf(out, "\tWARNING: Auto-tuned scene drop of %d is below the minimum of %d; recomputing candidates...\n",
			effectiveMinDrop, cmd.Int(minDropFlagName),
		)
		candidates = core.FilterCandidatesByDrop(allCandidates, cmd.Int(minDropFlagName))
		effectiveMinDrop = cmd.Int(minDropFlagName)
	}
	fmt.Fprintf(out, "\tScene drop auto-tuned to %d to stay within %s=%d, producing %d candidates\n",
		effectiveMinDrop, maxCandidatesFlagName, cmd.Int(maxCandidatesFlagName), len(candidates),
	)
	// Split results for callers
	thresholds = make([]float64, len(candidates))
	segmentations = make([][]ffmpeg.Scene, len(candidates))
	for i, candidate := range candidates {
		// A candidate threshold is a scene score as printed by ffmpeg: it must be converted to be
		// reusable with encode. Scenes are already computed, only the reported value changes.
		thresholds[i] = pipeline.ReusableThreshold(candidate.Threshold, cmd.Float64(minThresholdFlagName))
		segmentations[i] = pipeline.FromCoreScenes(candidate.Scenes)
	}
	return
}
