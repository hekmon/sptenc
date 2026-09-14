package core

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/hekmon/sptenc/ng/ffmpeg"

	"al.essio.dev/pkg/shellescape"
	"github.com/hekmon/cunits/v3"
)

const (
	segEncodedOutputFormat = "seg_%d_qp%d.mkv"
)

// QPSearchCallbacks is implemented by the caller to observe and present the search process.
type QPSearchCallbacks interface {
	// Logging
	Debug(format string, a ...any)
	Warning(format string, a ...any)
	Error(err error)
	// Segment lifecycle
	OnSegmentStart(segmentIndex int, segmentPath string)
	OnSegmentNewCandidate(qpCandidate int)
	OnSegmentAnalysisStart(fileSize cunits.Bits)
	OnSegmentAnalysisProgress(read cunits.Bits) // not total, additionnal
	OnSegmentAnalysisStop()
	OnSegmentEncodeStart(totalFrames int)
	OnSegmentEncodeProgress(stats ffmpeg.ProgressStats)
	OnSegmentEncodeStop()
	OnSegmentVMAFStart(totalFrames int)
	OnSegmentVMAFProgress(stats ffmpeg.ProgressStats)
	OnSegmentVMAFStop()
	OnSegmentDone(segmentFinalQP, segmentFrames, segmentNbAttempts int, currentTotalDuration time.Duration, currentTotalSize cunits.Bits)
}

// QPSearchConfig holds the invariants for a QP search run.
type QPSearchConfig struct {
	// SegmentsPaths contains the file paths of the video segments to search for optimal QP.
	SegmentsPaths []string
	// Auditor validates whether a candidate QP meets the target quality (e.g. via VMAF).
	Auditor VMAFChecker
	// WorkingDir is the directory where temporary encoded segments and VMAF reports are written during the search.
	WorkingDir string
	// StatsCache holds previous QP search statistics to guide and accelerate the search.
	StatsCache *StatsCacheHistory
	// KeepInvalidQP, if true, retains encoded segments with non-selected QPs instead of deleting them.
	KeepInvalidQP bool

	// Encoder is the FFmpeg encoder to use for test encodes.
	Encoder ffmpeg.Encoder
	// NVIDIAGPUIndex is the CUDA device index to use for NVIDIA NVENC encoding.
	// If unset, it defaults to 0.
	NVIDIAGPUIndex int
	// VAAPIRendererPath is the DRM render node path to use for VA-API encoding.
	// If unset, it defaults to "/dev/dri/renderD128".
	VAAPIRendererPath string
	// D3D12VAGPUIndex is the Direct3D 12 adapter index to use for D3D12VA encoding.
	// If unset, it defaults to 0.
	D3D12VAGPUIndex int

	// VMAFNeg, if true, uses VMAF NEG (No Enhancement Gain) models.
	// Recommended when the source has undergone upscaling, sharpening, or denoising,
	// as these can artificially inflate standard VMAF scores.
	VMAFNeg bool
	// VMAFCUDA, if true, enables CUDA acceleration for VMAF computation.
	// This requires libvmaf to have been compiled with CUDA support.
	VMAFCUDA bool
}

// QPSearchResults holds the outcome of a QP search across all segments.
type QPSearchResults struct {
	// EncodedSegmentsPaths contains the file paths of the encoded segments that met the quality target.
	EncodedSegmentsPaths []string
	// QPs contains the selected QP value for each segment (aligned with EncodedSegmentsPaths).
	QPs []int
	// GlobalWeightedQP is the average QP across all segments, weighted by each segment's frame count.
	GlobalWeightedQP float64
	// TotalNbAttempts is the total number of encode attempts made during the search.
	TotalNbAttempts int
	// TotalSegmentsFrames is the sum of frame counts across all source segments.
	TotalSegmentsFrames int
	// TotalEncodedFrames is the sum of frame counts across all encode attempts (even from non selected qp encodes).
	TotalEncodedFrames int
	// NbBestEfforts is the number of segments that stop at the minimum QP without reaching the target VMAF profile.
	NbBestEfforts int
}

// FindAllSegmentsQP searches for the optimal QP for each segment.
func FindAllSegmentsQP(ctx context.Context, scb QPSearchCallbacks, config QPSearchConfig) (results QPSearchResults, err error) {
	// Prepare
	var (
		segmentDuration, doneDuration time.Duration
		segmentFrames                 int
		segmentQP, segmentWeights     int
		segmentSize, allSegmentSize   cunits.Bits
		segmentNbAttempts             int
		bestEffort                    bool
	)
	results.EncodedSegmentsPaths = make([]string, len(config.SegmentsPaths))
	results.QPs = make([]int, len(config.SegmentsPaths))
	// Go
	for segment, segmentPath := range config.SegmentsPaths {
		scb.OnSegmentStart(segment, segmentPath)
		// Find this segment QP
		if segmentQP, segmentFrames, segmentNbAttempts, bestEffort, segmentDuration, err = findSegmentQP(ctx, scb, config, segment, segmentPath); err != nil {
			err = fmt.Errorf("failed to find the right encoding QP for segment %d: %w", segment, err)
			return
		}
		encodedSegmentPath := filepath.Join(config.WorkingDir, fmt.Sprintf(segEncodedOutputFormat, segment, segmentQP))
		if segmentSize, err = getFileSize(encodedSegmentPath); err != nil {
			err = fmt.Errorf("failed to get the size of segment %d: %w", segment, err)
			return
		}
		// Update stats
		results.EncodedSegmentsPaths[segment] = encodedSegmentPath
		results.QPs[segment] = segmentQP
		results.TotalSegmentsFrames += segmentFrames
		results.TotalEncodedFrames += segmentFrames * segmentNbAttempts
		results.TotalNbAttempts += segmentNbAttempts
		if bestEffort {
			results.NbBestEfforts++
		}
		doneDuration += segmentDuration
		allSegmentSize += segmentSize
		segmentWeights += segmentQP * segmentFrames
		// Done
		scb.OnSegmentDone(segmentQP, segmentFrames, segmentNbAttempts, doneDuration, allSegmentSize)
	}
	results.GlobalWeightedQP = float64(segmentWeights) / float64(results.TotalSegmentsFrames)
	return
}

func findSegmentQP(ctx context.Context, scb QPSearchCallbacks, config QPSearchConfig,
	segment int, segmentPath string) (
	finalQP, segmentFrames, nbAttempts int, bestEffort bool, duration time.Duration, err error) {
	scb.Debug("Segment %d: Search for the right QP", segment)
	// Prepare
	segmentInfos, err := getStreamsInfosCF(ctx, scb, segmentPath)
	if err != nil {
		err = fmt.Errorf("failed to get streams infos: %w", err)
		return
	}
	duration = segmentInfos.Format.Duration
	videoTrack := segmentInfos.VideoTrack()
	// Abort if frame count is 0 or negative
	totalFrames := videoTrack.NbReadFrames
	if totalFrames <= 0 {
		totalFrames = videoTrack.NbFrames
		if totalFrames <= 0 {
			err = fmt.Errorf("segment %d: frame count is 0 or negative (Nb(Read)Frames: %d, duration: %s, frameRate: %s). Cannot proceed without valid frame count",
				segment, totalFrames, duration, videoTrack.RFrameRate,
			)
			return
		}
	}
	// Verify output files frames count when done
	defer func() {
		if err != nil {
			// if we exit with an error, no need to check that everything is fine
			return
		}
		finalQPSegmentPath := filepath.Join(config.WorkingDir, fmt.Sprintf(segEncodedOutputFormat, segment, finalQP))
		var finalSegmentInfos ffmpeg.FFProbeStats
		if finalSegmentInfos, err = getStreamsInfosCF(ctx, scb, finalQPSegmentPath); err != nil {
			// make findSegmentQP return an error
			err = fmt.Errorf("failed to get streams infos of final segment: %w", err)
			return
		}
		if segmentFrames = finalSegmentInfos.VideoTrack().NbReadFrames; segmentFrames != totalFrames {
			// make findSegmentQP return an error
			err = fmt.Errorf("final segment has %d frames instead of %d", segmentFrames, totalFrames)
			return
		}
		scb.Debug("Final segment has %d frames, as original GOP.", segmentFrames)
	}()
	// Search
	var testedQPs []int
	if !config.KeepInvalidQP {
		// Delete invalid QPs once finished
		defer func() {
			for _, testedQP := range testedQPs {
				if testedQP == finalQP {
					continue
				}
				invalidQPPath := filepath.Join(config.WorkingDir, fmt.Sprintf(segEncodedOutputFormat, segment, testedQP))
				if err := os.Remove(invalidQPPath); err != nil {
					scb.Error(fmt.Errorf("Failed to remove %s: %s", shellescape.Quote(invalidQPPath), err))
				}
			}
		}()
	}
	if finalQP, nbAttempts, bestEffort, testedQPs, err = searchSegmentQP(ctx, scb, config, segment, segmentPath, videoTrack); err != nil {
		err = fmt.Errorf("failed to search segment QP: %w", err)
		return
	}
	if bestEffort {
		scb.Warning("Segment #%d: Impossible to validate VMAF config with lowest possible QP (highest quality), keeping it anyway", segment)
	}
	return
}

func getStreamsInfosCF(ctx context.Context, scb QPSearchCallbacks, filePath string) (
	stats ffmpeg.FFProbeStats, err error) {
	// Recover size
	fileInfos, err := os.Stat(filePath)
	if err != nil {
		err = fmt.Errorf("failed to stat the file: %w", err)
		return
	}
	// Prepare signals
	scb.OnSegmentAnalysisStart(cunits.ImportInBytes(float64(fileInfos.Size())))
	defer scb.OnSegmentAnalysisStop()
	// Start analysis
	return ffmpeg.GetStreamsInfosCF(ctx, ffmpeg.GetStreamsInfosCFConfig{
		GetStreamsInfosConfig: ffmpeg.GetStreamsInfosConfig{
			// Input
			Path: filePath,
			// Reporting
			Debug: func(s string) {
				scb.Debug(s)
			},
			RuntimeError: scb.Error,
		},
		ReadBytesReport: func(bytesRead int) {
			scb.OnSegmentAnalysisProgress(cunits.ImportInBytes(float64(bytesRead)))
		},
	})
}

// searchSegmentQP finds the highest valid QP (smallest file) for a segment.
//
// The algorithm intentionally keeps each phase (bracketing, interpolation,
// boundary walks) explicit and inline. Edge-case handling is subtle;
// resist collapsing into generic helpers — readability trumps brevity here.
//
// The QP→VMAF relationship is empirically monotonic (lower QP = higher VMAF).
// This has held across 2+ years of production encoding; non-monotonic edge cases
// have not been observed in practice.
func searchSegmentQP(ctx context.Context, scb QPSearchCallbacks, config QPSearchConfig,
	segment int, segmentPath string, videoTrack *ffmpeg.FFProbeBinaryStream) (
	finalQP int, nbAttempts int, bestEffort bool, testedQPs []int, err error) {
	// Keep track of tested QPs
	qpMin, qpMax, found := ffmpeg.GetEncoderQPRange(config.Encoder)
	if !found {
		err = fmt.Errorf("failed to get QP range for encoder %s", config.Encoder)
		return
	}
	testedQPs = make([]int, 0, qpMax-qpMin+1) // ordered
	results := make(map[int]ffmpeg.VMAFStats, qpMax-qpMin+1)
	defer func() {
		scb.Debug("QPs tested: %+v", testedQPs)
	}()
	// Search loop
	var (
		candidateQP                                          int
		alreadyComputed, bestValidTested, firstInvalidTested bool
		vmafStats                                            ffmpeg.VMAFStats
	)
	bestValid := qpMin
	firstInvalid := qpMax
	mean, stddev := config.StatsCache.GetMeanStdDev()
	for {
		// Find a candidate
		_, bestValidTested = results[bestValid]
		_, firstInvalidTested = results[firstInvalid]
		if len(testedQPs) == 0 {
			// Step 1: test the mean as the starting point to determine the search direction.
			// If stats are out of the encoder range, the encode fails fast. Rotten data is
			// caught cheaply — no need to defensively clamp.
			candidateQP = mean
			scb.Debug("Searching for QP in range [%d, %d] with %d as first candidate", bestValid, firstInvalid, candidateQP)
		} else if !(bestValidTested && firstInvalidTested) {
			// Step 2: close the range. A valid result raises bestValid; an invalid one lowers firstInvalid.
			// This leaves one bound at its original extreme, signaling which direction to search.
			// Step from the mean in stddev increments until we bracket the threshold.
			// Once both sides are known, interpolation walks from invalid toward valid to find
			// the highest valid QP — the one that yields the smallest file.
			if bestValid == qpMin {
				if candidateQP = mean - len(results)*stddev; candidateQP < qpMin {
					if _, alreadyComputed = results[qpMin]; alreadyComputed {
						finalQP = qpMin
						bestEffort = true
						return
					}
					candidateQP = qpMin
				}
			} else if firstInvalid == qpMax {
				if candidateQP = mean + len(results)*stddev; candidateQP > qpMax {
					if _, alreadyComputed = results[qpMax]; alreadyComputed {
						finalQP = qpMax
						return
					}
					candidateQP = qpMax
				}
			} else {
				// should not happen
				err = fmt.Errorf("invalid devstdinterpol state: bestValidTested=%t (%d), firstInvalidTested=%t (%d)", bestValidTested, bestValid, firstInvalidTested, firstInvalid)
				return
			}
		} else {
			// Step 3: the range is closed — narrow it with interpolation.
			if candidateQP, err = interpolateCandidate(scb, config, bestValid, firstInvalid, qpMin, qpMax, results); err != nil {
				err = fmt.Errorf("failed to find candidate: %w", err)
				return
			}
			// Handle predicted candidate.
			// A valid QP is not optimal until the next higher QP is confirmed invalid.
			// The boundary must be found, not just any valid point.
			if vmafStats, found = results[candidateQP]; found {
				scb.Debug("Predicted candidate %d already computed (valid: %t)", candidateQP, config.Auditor.Validate(vmafStats))
				// We already computed this candidate, let's think this thru
				if config.Auditor.Validate(vmafStats) {
					if candidateQP == qpMax {
						// Can not go higher, we are done
						finalQP = candidateQP
						return
					}
					// Are we sure that next higher candidate does not validate ?
					for i := candidateQP + 1; i <= qpMax; i++ {
						if vmafStats, found = results[i]; found {
							// we already computed this candidate
							if i == qpMax {
								// we reached qp max, which is already computed, we are done
								finalQP = i
								return
							}
							if !config.Auditor.Validate(vmafStats) {
								// Invalid, previous was the last valid
								finalQP = i - 1
								return
							}
							// else continue to go up
							scb.Debug("Looking up: candidate %d already computed (valid: %t)", i, true)
						} else {
							// we found a candidate for smaller size that we did not compute yet
							candidateQP = i
							break
						}
					}
				} else {
					// the predicted candidate is already computed and it does not validate
					if candidateQP == qpMin {
						// can not go lower, and does not validate: we are done (best effort)
						finalQP = candidateQP
						bestEffort = true
						return
					}
					// Let's go down one by one until it validates
					for i := candidateQP - 1; i >= qpMin; i-- {
						if vmafStats, found = results[i]; found {
							// we already computed this candidate
							if i == qpMin {
								// we reached qp min, which is already computed, we are done (best effort)
								finalQP = i
								bestEffort = true
								return
							}
							if config.Auditor.Validate(vmafStats) {
								// This already computed lower QP is valid, no need to go lower
								finalQP = i
								return
							}
							// else continue to go down
							scb.Debug("Looking down: candidate %d already computed (valid: %t)", i, false)
						} else {
							// we found a candidate for better quality that we did not compute yet
							candidateQP = i
							break
						}
					}
				}
			} else {
				scb.Debug("Predicted candidate %d selected for computation", candidateQP)
			}
		}
		// Test candidate and narrow the search
		scb.OnSegmentNewCandidate(candidateQP)
		if vmafStats, err = segmentQP(ctx, scb, config, segmentPath, segment, candidateQP, videoTrack); err != nil {
			err = fmt.Errorf("failed to produce QP %d: %w", candidateQP, err)
			return
		}
		nbAttempts++
		results[candidateQP] = vmafStats
		if config.Auditor.Validate(vmafStats) {
			bestValid = candidateQP
		} else {
			firstInvalid = candidateQP
		}
		testedQPs = append(testedQPs, candidateQP)
	}
}

func interpolateCandidate(scb QPSearchCallbacks, config QPSearchConfig,
	bestValid, firstInvalid, encoderQPMin, encoderQPMax int, existingResults map[int]ffmpeg.VMAFStats) (
	candidateQP int, err error) {
	predicator, err := NewPredicator(existingResults, encoderQPMin, encoderQPMax, scb.Debug)
	if err != nil {
		err = fmt.Errorf("failed to create predicator: %w", err)
		return
	}
	// Walk the bracket from the invalid side down to the valid side.
	// Prefer real results; predict only when missing.
	// Return the first candidate that validates.
	var (
		candidateResults ffmpeg.VMAFStats
		exists           bool
	)
	for candidateQP = firstInvalid; candidateQP > bestValid; candidateQP-- {
		if candidateResults, exists = existingResults[candidateQP]; !exists {
			if candidateResults, err = predicator.Predict(candidateQP); err != nil {
				err = fmt.Errorf("failed to predict QP %d (within %d-%d): %w", candidateQP, bestValid, firstInvalid, err)
				return
			}
			// Predicted result will be validated below
		}
		if config.Auditor.Validate(candidateResults) {
			// Found a validating candidate within the bracket, either a real result or a forecast.
			// Return it for the search loop to handle.
			return
		}
	}
	// Nothing in the bracket validated, fall back to the known-good bestValid.
	scb.Debug("No candidate found in range %d-%d, returning %d", bestValid, firstInvalid, candidateQP)
	return
}

func segmentQP(ctx context.Context, scb QPSearchCallbacks, config QPSearchConfig,
	input string, segment, qp int, videoTrack *ffmpeg.FFProbeBinaryStream) (
	vmafStats ffmpeg.VMAFStats, err error) {
	output := filepath.Join(config.WorkingDir, fmt.Sprintf(segEncodedOutputFormat, segment, qp))
	// Encode
	if err = segmentQPEncode(ctx, scb, config, input, output, qp, videoTrack); err != nil {
		err = fmt.Errorf("failed to encode segment: %w", err)
		return
	}
	// Compute VMAF
	vmafReport, err := segmentVMAF(ctx, scb, config, input, output, videoTrack)
	if err != nil {
		err = fmt.Errorf("failed to compute VMAF for segment: %w", err)
		return
	}
	vmafStats = vmafReport.GetStats()
	scb.Debug("Segment %d: QP %d: VMAF results:\n%s", segment, qp, vmafStats)
	return
}

func segmentQPEncode(ctx context.Context, scb QPSearchCallbacks, config QPSearchConfig,
	input, output string, qp int, videoTrack *ffmpeg.FFProbeBinaryStream) (err error) {
	// Signal start & stop
	scb.OnSegmentEncodeStart(videoTrack.NbReadFrames)
	defer scb.OnSegmentEncodeStop()
	// Execute the requested encoder
	start := time.Now()
	switch config.Encoder {
	// HEVC
	case ffmpeg.HEVCEncoderLibx265:
		err = ffmpeg.HEVCLibx265EncodeQP(ctx, ffmpeg.HEVCLibx265EncodeQPConfig{
			Input:        input,
			Preset:       ffmpeg.Libx265PresetSlow,
			Quantization: qp,
			Output:       output,
			Debug: func(msg string) {
				scb.Debug(msg)
			},
			RuntimeError:      scb.Error,
			FFMPEGStatsReport: scb.OnSegmentEncodeProgress,
		})
	case ffmpeg.HEVCEncoderNVEnc:
		err = ffmpeg.HEVCNVEncEncodeQP(ctx, ffmpeg.HEVCNVEncEncodeQPConfig{
			Input:        input,
			Device:       config.NVIDIAGPUIndex,
			Preset:       ffmpeg.NVEncPresetP7,
			Quantization: qp,
			Output:       output,
			Debug: func(msg string) {
				scb.Debug(msg)
			},
			RuntimeError:      scb.Error,
			FFMPEGStatsReport: scb.OnSegmentEncodeProgress,
		})
	case ffmpeg.HEVCEncoderVAAPI:
		err = ffmpeg.HEVCVAAPIEncodeQP(ctx, ffmpeg.HEVCVAAPIEncodeQPConfig{
			Input:        input,
			Device:       config.VAAPIRendererPath,
			Quantization: qp,
			Output:       output,
			Debug: func(msg string) {
				scb.Debug(msg)
			},
			RuntimeError:      scb.Error,
			FFMPEGStatsReport: scb.OnSegmentEncodeProgress,
		})
	case ffmpeg.HEVCEncoderD3D12VA:
		err = ffmpeg.HEVCD3D12VAEncodeQP(ctx, ffmpeg.HEVCD3D12VAEncodeQPConfig{
			Input:        input,
			Device:       config.D3D12VAGPUIndex,
			Quantization: qp,
			Output:       output,
			Debug: func(msg string) {
				scb.Debug(msg)
			},
			RuntimeError:      scb.Error,
			FFMPEGStatsReport: scb.OnSegmentEncodeProgress,
		})
	// AV1
	case ffmpeg.AV1EncoderLibaom:
		err = ffmpeg.AV1LibaomEncodeQP(ctx, ffmpeg.AV1LibaomEncodeQPConfig{
			Input:        input,
			CPUUsed:      ffmpeg.AV1LibaomCPUUsedDefault,
			Quantization: qp,
			Output:       output,
			Debug: func(msg string) {
				scb.Debug(msg)
			},
			RuntimeError:      scb.Error,
			FFMPEGStatsReport: scb.OnSegmentEncodeProgress,
		})
	case ffmpeg.AV1EncoderSVTAV1:
		err = ffmpeg.AV1SVTAV1EncodeQP(ctx, ffmpeg.AV1SVTAV1EncodeQPConfig{
			Input:        input,
			Preset:       ffmpeg.AV1SVTAV1PresetDefault,
			Quantization: qp,
			Output:       output,
			Debug: func(msg string) {
				scb.Debug(msg)
			},
			RuntimeError:      scb.Error,
			FFMPEGStatsReport: scb.OnSegmentEncodeProgress,
		})
	case ffmpeg.AV1EncoderNVEnc:
		err = ffmpeg.AV1NVEncEncodeQP(ctx, ffmpeg.AV1NVEncEncodeQPConfig{
			Input:        input,
			Device:       config.NVIDIAGPUIndex,
			Preset:       ffmpeg.NVEncPresetP7,
			Quantization: qp,
			Output:       output,
			Debug: func(msg string) {
				scb.Debug(msg)
			},
			RuntimeError:      scb.Error,
			FFMPEGStatsReport: scb.OnSegmentEncodeProgress,
		})
	case ffmpeg.AV1EncoderVAAPI:
		err = ffmpeg.AV1VAAPIEncodeQP(ctx, ffmpeg.AV1VAAPIEncodeQPConfig{
			Input:        input,
			Device:       config.VAAPIRendererPath,
			Quantization: qp,
			Output:       output,
			Debug: func(msg string) {
				scb.Debug(msg)
			},
			RuntimeError:      scb.Error,
			FFMPEGStatsReport: scb.OnSegmentEncodeProgress,
		})
	default:
		return fmt.Errorf("unsupported encoder: %q", string(config.Encoder))
	}
	// Done
	if err == nil {
		scb.Debug("Segment encoded in %s", time.Since(start).Round(time.Second))
	}
	return
}

func segmentVMAF(ctx context.Context, scb QPSearchCallbacks, config QPSearchConfig,
	segmentOriginal, segmentEncoded string, videoTrack *ffmpeg.FFProbeBinaryStream) (stats ffmpeg.VMAFReport, err error) {
	scb.OnSegmentVMAFStart(videoTrack.NbReadFrames)
	defer scb.OnSegmentVMAFStop()
	return ffmpeg.VMAFCompute(ctx, ffmpeg.VMAFComputeConfig{
		ReferencePath:     segmentOriginal,
		DistortedPath:     segmentEncoded,
		InputFrameRate:    videoTrack.RFrameRate,
		ReportPath:        segmentEncoded + "_vmaf.json",
		UltraHD:           videoTrack.Height >= ffmpeg.UltraHDHeight,
		NoEnhancementGain: config.VMAFNeg,
		NVDECReference:    ffmpeg.IsNVDecCompatible(videoTrack.CodecName) && config.VMAFCUDA, // rare: only when user supplies their own segments (advanced mode). Default slicing produces FFV1 which is not NVDEC-compatible.
		NVDECDistorted:    config.VMAFCUDA,                                                   // encoded segments are in HEVC or AV1, both can be decoded by nvdec so the question is: is there a nvidia GPU ? If user requested vmafCUDA we know for sure
		VMAFCuda:          config.VMAFCUDA,
		GPUID:             &config.NVIDIAGPUIndex,
		Debug: func(msg string) {
			scb.Debug(msg)
		},
		RuntimeError:      scb.Error,
		FFMPEGStatsReport: scb.OnSegmentVMAFProgress,
	})
}
