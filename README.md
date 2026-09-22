# Split Encoder

`sptenc` (Split Encoder) is a scene-aware, [VMAF](https://github.com/Netflix/vmaf)-driven video transcoder for enthusiasts who want every scene of their encodes to meet a measured quality target, at the smallest size that still meets it, and accept to pay for that in encoding time with a CPU encoder, or in file size with a GPU one.

It splits the input into scene-aligned segments, encodes each one independently, and validates the result against configurable VMAF thresholds before accepting it.
Failed segments are automatically re-encoded at a lower QP until all thresholds are met.
A final, complete VMAF comparison between the encoded output and original source is performed at the end, and its results are embedded into the output file's metadata tags.

This approach gives each scene the highest QP, so the smallest size, that still passes the quality target defined by the VMAF profile.

> **Trade-off:** several QP values are tested on every segment and each one is measured with VMAF, so an encode costs a few passes of the chosen encoder where a CRF encode costs one. What that costs depends on the encoder: a larger file with a GPU encoder, which searches faster than realtime once several segments run in parallel, hours and the smallest file with a CPU one (measured in [Encoder selection vs file size](#encoder-selection-vs-file-size)). The floor is the same either way, and it is not what a single CRF pass with a whole-file VMAF check gives: that only validates an average, so one complex scene in an otherwise steady movie can be destroyed while the overall score still looks fine. sptenc enforces its quality floor on every single scene independently.

> **Inspiration:** sptenc is inspired by Netflix's [Dynamic Optimizer](https://netflixtechblog.com/dynamic-optimizer-a-perceptual-video-encoding-optimization-framework-e19f1e3a277f) framework, which pioneered scene-aware, perceptually-optimized video encoding, and the VMAF perceptual quality models that power it. This project is built for power users encoding on their own hardware, not for streaming-scale infrastructure.

## Who is this for?

You probably don't need sptenc if you just want to shrink a video for your phone. Standard tools like HandBrake or ffmpeg with CRF are faster and perfectly fine for that.

sptenc is built for workflows where you want the **smallest file size that still meets a VMAF floor you can prove**:

- **Archival & preservation** — You have a high-bitrate source or lossless master (or an expensive AI-upscaled restoration) and want to compress it without ever dropping below a VMAF floor you can prove.
- **Quality-per-bit optimization** — You target a specific visual fidelity at the smallest size and currently do manual CRF sweeps, screenshot comparisons, or test encodes to find the right settings. sptenc automates that search and produces a VMAF report documenting the result.
- **Large collection processing** — You process many files against a single, carefully tuned quality profile. sptenc treats that profile as a contract: every segment is encoded, measured, and corrected until it passes, without you checking scores by hand.
- **NAS / media server optimization** — You maintain a personal library of high-bitrate remuxes and need to balance quality against storage costs. sptenc replaces manual CRF trial-and-error with a measurable guarantee, so you keep the quality that matters and reclaim the space that doesn't.

If you already know why CRF averages can hide bad frames, sptenc closes the loop: encode, measure, correct, converge.

## Key Features

- 🎯 **VMAF-driven encoding** - The quality gate is a perceptual score measured on the output, not a CRF or bitrate you hope will be enough. 1080p or 4K model auto-selected from the input, NEG variants available (see [what VMAF does not see](#what-vmaf-does-not-see))
- 🎬 **Scene-aware segmentation** - Segments are cut on scene changes, so the quality floor is enforced per scene, never averaged across a whole file
- 📊 **Multi-metric validation** - Combine mean, harmonic mean, median, percentiles (P1/P5/P10/P25) and worst frame; every enabled threshold must pass
- 🧠 **Adaptive QP search** - Each segment converges on the highest QP that still passes in a few attempts, and stats kept from previous runs make the next ones start closer (see [Adaptive QP Search](#adaptive-qp-search))
- ⚡ **CPU and GPU encoders** - HEVC and AV1 with `libx265`, `libsvtav1`, NVENC, VAAPI, D3D12VA and VideoToolbox. Prototype a VMAF profile fast on the GPU and encode the final file small on the CPU, or keep the GPU encode when time matters more than size: the floor is proven the same way (see [Encoders](#encoders))
- 🚀 **Hardware acceleration wherever it helps** - CUDA VMAF, GPU decoding even alongside a CPU encoder, and several segments searched in parallel: same output, less time
- 🔬 **Automatic scene threshold search** - `batchsearch` tries several scene detection thresholds and keeps the one that produces the smallest passing file
- 📋 **A file you can trust** - Audio, subtitles and color metadata are carried over (PCM audio losslessly compressed to FLAC), and the final whole-file VMAF result is written into the output's metadata tags

## How It Works

1. **Scene detection** - FFmpeg `scdet` analyzes the video to find scene boundaries, producing semantically coherent segments. For precise frame-accurate cuts, a lossless FFV1 master is used: every frame is self-contained, so splits can happen at any frame without quality loss or dropped frames.
2. **Per-segment encoding** - Each segment is encoded independently with the chosen encoder (e.g. `libx265`, `hevc_nvenc`, `libsvtav1`).
3. **VMAF validation (post-encode)** - After encoding, each segment's VMAF scores are computed and checked against all configured thresholds. Any failure triggers a re-encode at a lower QP.
4. **Adaptive QP search** - Each segment starts from a smart QP estimate, brackets the valid range with stepped probes, then uses interpolation to converge on the highest valid QP (smallest file) in just a few attempts. Persistent stats from previous runs further accelerate this (see below).
5. **Best effort** - If the encoder's minimum QP is reached and thresholds are still not met (e.g. pathological scene), the segment is accepted and flagged as "best effort" in logs.
6. **Muxing & tagging** - Segments are merged into a single output file. Audio, subtitles, and other streams from the original source are remuxed into the final file. PCM audio tracks are automatically losslessly compressed to FLAC. A final VMAF comparison between the complete encoded file and original source is performed, with results displayed in logs and embedded in the output file's metadata tags. Matroska statistics tags are regenerated for full player compatibility.

## How does it compare to Av1an?

[Av1an](https://github.com/rust-av/Av1an) is the reference tool for scene based chunked encoding, and its Target Quality mode looks like what sptenc does: find, for each scene, the encoder setting reaching a metric score. If you know Av1an, this is the question you have. Both tools overlap, they do not aim at the same thing. As of Av1an's documentation and sources in September 2026:

| | Av1an (Target Quality) | sptenc |
|---|---|---|
| Purpose | Encode faster by running several encoder processes in parallel; Target Quality is one of its modes | Enforce a quality floor on every scene; time (CPU encoder) or size (GPU encoder) is what is traded for it |
| What is promised for a scene | A score to aim at: a limited number of probes (4 by default), and the probe closest to the target is used if none reached it | A floor: no limit on attempts, a segment is only accepted once it passes, or is flagged as best effort when even the lowest QP of the encoder can not pass |
| What is measured | Probes, by default faster and lower resolution encodes than the final one, which is then not measured (`--probe-slow` makes the probes real encodes) | The segments ending up in the output file, always |
| Quality gate | One statistic of one metric (mean, harmonic mean, a percentile, minimum...) | Any combination of 8 statistics, all having to pass |
| Metrics | VMAF, SSIMULACRA2, Butteraugli, XPSNR | VMAF only |
| Dial | CRF / CQ, the rate control of the encoder stays in charge | Constant QP (see [why](#why-qp-instead-of-crf)) |
| Encoders | Software: aomenc, SVT-AV1, rav1e, vpxenc, x264, x265, with your own parameters | libx265, SVT-AV1 and hardware encoders (NVENC, VAAPI, D3D12VA, VideoToolbox), with fixed opinionated parameters |
| GPU | Decoding (DGDecNV) and some metrics | Encoding, to search fast then encode the final file on CPU (`batchsearch --final-encode`) or as the final encoder, and VMAF (CUDA) |
| Scene cuts | av-scenechange, frame exact chunks piped through VapourSynth, no intermediate file needed | ffmpeg `scdet`, frame exact cuts of a lossless intermediate (large: count on disk space), and a search of the scene threshold itself (`batchsearch`) |
| Verification | Optional VMAF plot of the result | Frame counts of every segment and of the final file, final VMAF of the whole file embedded in its tags |
| Learning | None between runs | QP statistics of previous encodes kept to start the next searches closer |
| Interrupted run | Can be resumed | Starts over |
| Requirements | FFmpeg, VapourSynth, the encoders binaries | ffmpeg (with libvmaf) and mkvpropedit |

In short: choose Av1an to encode fast and well, with the encoder, the parameters and the metric of your choice, a metric seeing color (SSIMULACRA2, Butteraugli) included. Choose sptenc when the point is not to get close to a score but to never get under it, on the very files you will keep, and to have that written in them.

## Why QP instead of CRF?

sptenc controls quality with **QP (Quantization Parameter)** in **CQP (Constant QP)** mode, not CRF.

This is not because CRF could not be searched: for a given segment, both dials are deterministic (same value, same file) and monotonic (VMAF goes down as the value goes up), which is all the interpolation search needs to converge in a few attempts. The reasons are elsewhere:

- **One dial for every encoder.** CRF is a software encoder concept. Hardware encoders expose a constant quantizer, or their own flavor of quality target, not CRF. With QP, the same search, the same statistics and the same workflow (search on a GPU encoder, final encode on its CPU counterpart) apply to every supported encoder.
- **No rate control competing with the search.** CRF is a rate control: the encoder moves bits between frames and blocks following its own perceptual model (adaptive quantization, cu-tree), which is not VMAF. sptenc already has something deciding where quality must vary, against the metric you chose: the scene splitter, then the search of each segment. A segment being a single scene, its content is homogeneous, there is not much left for a rate control to adapt to.

What happens around that base QP depends on the encoder. `libx265` turns adaptive quantization and cu-tree off by itself in constant QP mode, whatever is asked: the QP requested is the QP applied, frame type offsets aside. NVENC encoders keep their spatial and temporal adaptive quantization (and their lookahead) active under `constqp`: the QP requested is a base the driver modulates per block. Either way these settings are **identical for every tested QP**, only the base QP moves, so the comparison between candidates remains stable.

Whether CRF would give a smaller or a bigger file at the same VMAF score depends on the content and is not something sptenc relies on. The guarantee does not come from the dial anyway: it comes from measuring every segment after it has been encoded, and encoding it again when it fails.

## Commands

sptenc is organized into subcommands. Run `sptenc <command> --help` for detailed usage of each.

| Command | Alias | Category | Purpose |
|---|---|---|---|
| `encode` | `e` | Main | Full pipeline: split (if needed), encode segments, validate VMAF, remux, and tag |
| `check` | `k` | Main | Check that ffmpeg, ffprobe, and mkvpropedit are present and list available encoders |
| `master` | `m` | Tooling | Create a lossless FFV1 intermediate from a source file for frame-accurate splitting |
| `split` | `s` | Tooling | Detect scene changes and split a video into separate segment files |
| `concat` | `c` | Tooling | Concatenate video files from a directory into a single file without re-encoding |
| `remux` | `r` | Tooling | Replace the video track of a file with a new one without re-encoding |
| `vmaf` | `v` | Tooling | Compute VMAF between a reference and a distorted video |
| `thresholds` | `t` | Tooling | Preview candidate thresholds and their scene distributions without encoding |
| `cache` | — | Tooling | Inspect and clear persistent QP statistics cache |
| `batchsearch` | `bs` | Advanced | Search for the scene threshold that yields the smallest passing file by encoding multiple candidates |

## Input Requirements

### Constant Frame Rate (CFR)
**Variable frame rate (VFR) content is not supported.** VMAF requires frame-exact alignment between reference and distorted videos. VFR content causes FFmpeg to duplicate or drop frames when forced to a constant rate, invalidating VMAF scores. sptenc rejects VFR inputs at startup.

### Progressive content
**Interlaced content is not supported.** Nothing in the pipeline handles fields: an interlaced source would be encoded as progressive frames with the combing baked in, in a file whose container still declares it interlaced while its frames say progressive. VMAF would validate it all the same (combed pictures compared with the same combed pictures). sptenc rejects inputs declared as interlaced at startup: deinterlace them first, the method being a choice on the content that is yours to make. This relies on what the stream declares: progressive pictures stored in an interlaced stream (PsF) are rejected too.

### Pre-segmented Input (Optional)

Instead of letting sptenc split the input automatically, you can provide an already-split directory of segments. Files must be `.mkv` or `.mp4` and are processed in **alphabetical order** — name them accordingly (e.g. `seg_01.mkv`, `seg_02.mkv`) to preserve scene order. All segments must share the same codec and frame rate.

```bash
./sptenc encode ./gop_dir/ output.mkv --original-file original_with_audio.mkv
```

When using a pre-segmented directory, `--original-file` (alias `-f`) is **required** so sptenc can remux audio, subtitles, and other streams into the final output.

## Quick Start

### Check your environment
```bash
./sptenc check
```

### Basic encode - VMAF harmonic mean ≥ 93 (default)
```bash
./sptenc encode video.mkv output.mkv
```

### Strict quality with multiple thresholds
```bash
./sptenc encode video.mkv output.mkv --vmaf-mean 95 --vmaf-p5 85 --vmaf-min 70
```

### Use the VMAF NEG models
```bash
./sptenc encode video.mkv output.mkv --vmaf-neg
```

### Fast VMAF profile prototyping with NVENC on the second GPU
```bash
./sptenc encode video.mkv output.mkv --encoder hevc_nvenc --vmaf-cuda --nvidia-gpu-index 1 --vmaf-mean 93
# Once happy with the profile, re-run with the default libx265 for a smaller file, or keep this one
```

### CPU encode with the GPU decoding on the side
```bash
# libx265 runs on the CPU: scene detection, the master, the frame counts and the distorted side
# of VMAF are decoded by NVDEC instead (a hardware encoder does this by itself)
./sptenc encode video.mkv output.mkv --nvdec --vmaf-mean 93
```

### Pre-segmented directory
```bash
./sptenc encode ./gop_dir/ output.mkv --original-file original_with_audio.mkv --vmaf-mean 95
```

### Concatenate segments without re-encoding
```bash
./sptenc concat ./segments/ merged.mkv
```

### Compute VMAF between two videos
```bash
# Software libvmaf with GPU-accelerated decoding
./sptenc vmaf --nvdec original.mkv encoded.mkv

# Full GPU acceleration (requires libvmaf_cuda)
./sptenc vmaf --vmaf-cuda --vmaf-neg original.mkv encoded.mkv
```

### Search for the threshold that yields the smallest passing file
```bash
# Fast search on GPU: the output is the best candidate, encoded by the GPU encoder
./sptenc batchsearch video.mkv output.mkv --encoder hevc_nvenc --vmaf-cuda --vmaf-hmean 93
# Same search, but once the best threshold is found the file is re-encoded with the equivalent
# CPU encoder to shrink it even further (one run: no need to run the search first)
./sptenc batchsearch video.mkv output.mkv --encoder hevc_nvenc --vmaf-cuda --vmaf-hmean 93 --final-encode
```

### Manual pipeline (master → split → encode)
```bash
./sptenc master video.mkv master.mkv
./sptenc split master.mkv ./segments/ --master --min-threshold 12
./sptenc encode ./segments/ output.mkv --original-file video.mkv
```

> Use the `thresholds` command to preview candidate thresholds and their scene distributions without encoding. Experiment with `--min-threshold` (0–100): higher values detect fewer scenes, lower values detect more. `split` and `encode` default to 10, ffmpeg's own; `thresholds` and `batchsearch` search upward from 8.
>
> **Use everything you have.** A hardware encoder decodes with its own GPU and `--vmaf-cuda` decodes with NVDEC: nothing to set. With a CPU encoder (the smallest files, out of the CPU alone), hand the decoding to whatever GPU is in the machine, integrated or Apple silicon included: `--nvdec`, `--vaapi-dec`, `--d3d12va-dec` or `--videotoolbox-dec`. Same file out, more CPU left for the encoder. The same flags serve `master`, `split`, `thresholds` and `vmaf`. You can not get it wrong: an unsupported source codec falls back to software with a warning, a flag contradicting the encoder is refused. Only the lossless FFV1 intermediate has no hardware decoder.

## Scene Detection and Threshold Selection

Scene detection splits a video into independent segments, and each segment gets its own QP. The threshold controls how many boundaries are kept, which directly affects both quality visibility and file size. There is no single right threshold — the choice depends on which tradeoff you are willing to accept.

### Too fine: many short segments

A low threshold keeps almost every detected boundary. This gives hard passages their own QP and keeps VMAF metrics honest — a bad frame in a 5-second segment will almost certainly move the segment's mean or percentiles enough to trigger a re-encode.

The cost is keyframe bloat and B/P-frame starvation. Every boundary forces an I-frame, and runs shorter than a few seconds never let inter-frame referencing amortize the intra cost. File size inflates, and VMAF percentile metrics become statistically unreliable on very short segments (p1 needs ≥100 frames, p5 needs ≥20).

### Too coarse: few long segments

A high threshold discards weak boundaries, merging scenes into long runs. B/P-frame compression thrives, and file size drops — but the whole segment must bow to its hardest passage. Easy sections pay for quality they do not need.

More dangerously, a short complex passage inside a long easy segment can fail VMAF locally while the segment-wide average still passes. The bad frames are statistically invisible, undermining the guarantee that every part of the video meets your quality floor.

### The role of `batchsearch`

`batchsearch` automates the tedious work of testing multiple thresholds and picking the one that produces the smallest file while still passing your VMAF targets. Its objective is file size — it has no opinion on whether the winning threshold's segment lengths are short enough for their percentile metrics to be trustworthy.

If you care more about tight quality control than file size, skip `batchsearch`. Use the `thresholds` command to inspect distributions, pick a threshold manually, and run `encode`.

### The `--min-segment-length` guardrail

The `--min-segment-length` flag (alias `-L`, default 5s) removes boundaries that would create segments shorter than the given duration. This is a quality-floor guardrail: it prevents unreliable percentile metrics and B/P-frame starvation by merging short segments into their shorter neighbour.

It does **not** protect against the opposite problem. Segments longer than ~5 seconds may still be too long for your tolerance of the drowning risk. That judgment remains yours.

Use the `thresholds` command to preview the segment distributions a threshold would produce before committing to an `encode` or a `batchsearch` run. It is fast and produces no files.

### Reusing a threshold

`encode`, `thresholds`, and `batchsearch` all build scenes the same way: the threshold picks the boundaries first, then `--min-segment-length` merges the short segments that remain. The scenes a threshold produces therefore depend only on that threshold and on `--min-segment-length` — not on the `--min-threshold`/`--max-threshold` range a search was run with.

This makes thresholds portable: a row of the `thresholds` table, or the best candidate reported by `batchsearch`, gives exactly the same scenes when passed to `encode -T` with the same `--min-segment-length`. A typical use is to run `batchsearch` on one episode and `encode -T <best>` on the rest of the season.

Reported thresholds look like `24.2765` rather than `24.277`: ffmpeg prints scene scores rounded to 3 decimals but compares thresholds against the unrounded score, so sptenc reports half a step below the printed score to guarantee the boundary is kept. Use the value as printed.

## VMAF

### Models

| Resolution | Standard Model | NEG Model |
|---|---|---|
| < 4K | `vmaf_v0.6.1` | `vmaf_v0.6.1neg` |
| ≥ 4K (2160p) | `vmaf_4k_v0.6.1` | `vmaf_4k_v0.6.1neg` |

The model is **automatically selected** based on input resolution. Use `--vmaf-neg` to select its NEG variant.

> **Note:** NEG stands for **No Enhancement Gain**. The standard `vmaf_v0.6.1` model predicts the viewing condition of a **1080p HDTV at 3 picture heights**, and `vmaf_4k_v0.6.1` that of a **4K TV at 1.5 picture heights** — keep this in mind when interpreting scores for other display formats. See the [VMAF documentation](https://github.com/Netflix/vmaf/blob/master/resource/doc/models_v0.md#disabling-enhancement-gain-neg-mode) for details.

### Quality Score Reference

VMAF scores are relative to the viewing conditions the models were trained on (1080p HDTV at 3 picture heights; 4K TV at 1.5 picture heights), so treat them as comparable only within the same viewing context. The following anchors come from published, verifiable sources:

| VMAF Score | Verifiable interpretation | Source |
|---|---|---|
| **95–100** | Subjectively indistinguishable from the original, on average | Peer-reviewed subjective study cited by Ozer |
| **~93** | "Indistinguishable from original or with noticeable but not annoying distortion" for the vast majority of viewers (4K test set) | RealNetworks white paper (*VMAF Reproducibility*), relayed by Ozer |
| **40–70** | Typical range of an SD encode at 480p | Netflix data cited by Ozer |
| **~20** | A 240p encode at CRF 28 | Netflix data cited by Ozer |

See [StreamingLearningCenter — Optimal encoding ladder with VMAF](https://streaminglearningcenter.com/encoding/optimal_encoding_ladder_vmaf.html) and [Netflix via StreamingLearningCenter — Just Noticeable Difference](https://streaminglearningcenter.com/codecs/finding-the-just-noticeable-difference-with-netflix-vmaf.html).

Intermediate bands commonly cited elsewhere (e.g. "80–90 = good quality with minor artifacts", "70–80 = medium quality") are editorial interpolations between these anchors, not verbatim quotes from a verifiable source — we intentionally do not print them here.

### What VMAF does not see

Every guarantee sptenc makes is a guarantee on a VMAF score: it is worth what the metric is worth. VMAF is a good predictor of perceived quality for what it was built for, compression and scaling artifacts on the picture structure, but it is blind to some defects. The first two are easy to reproduce with the `vmaf` command:

- **Color.** Only the luma plane is measured. A fully desaturated (grayscale) copy of a colorful video gets exactly the score of the video compared with itself, with the standard model and the NEG one alike. So does a copy with shifted hues, or with heavily blurred chroma. A defect only affecting the chroma planes can not fail a segment. In practice encoders quantize luma and chroma together, so a segment passing on luma is not expected to be damaged on chroma: this is an expectation, not something sptenc verifies.
- **Banding.** A smooth dark gradient reduced to 5 luma levels across the whole picture, as posterized as it gets, scores 100 with the standard model (more than the untouched gradient compared with itself: the added edges count as an enhancement) and still 92.5 with the NEG model. Banding has its own detector in libvmaf ([CAMBI](https://github.com/Netflix/vmaf/blob/master/resource/doc/cambi.md)), which sptenc does not use. The mandatory 10-bit output is there to avoid creating banding in the first place, not to detect it.
- **What happens between frames.** Frames are scored one by one, then the scores are pooled. A quality changing from a frame to the next (flicker, keyframe pulsing) is not judged as such, only the score of each frame is. The [VMAF FAQ](https://github.com/Netflix/vmaf/blob/master/resource/doc/faq.md) itself points out that viewers weigh the worst frames more than an arithmetic mean does: this is why the default gate is the harmonic mean, and why percentiles and minimum gates exist.
- **HDR.** The [models documentation](https://github.com/Netflix/vmaf/blob/master/resource/doc/models_v0.md) only describes SDR viewing conditions. sptenc computes VMAF on HDR sources as on any other, without any tone mapping: take these scores as an indication, not as a validated measure.

If your content is exposed to one of these (dark gradients, saturated animation, HDR), have a look at a few segments yourself before trusting the numbers and deleting a source.

### sptenc thresholds

VMAF scores range from 0 to 100. A difference of **~6 points ≈ 1 JND**
(Just Noticeable Difference — the change detectable by ~75% of viewers. Note that JND thresholds are content-dependent: academic work reports ΔVMAF step sizes from 2 to 6 points depending on content and methodology.) See [Netflix via StreamingLearningCenter — Just Noticeable Difference](https://streaminglearningcenter.com/codecs/finding-the-just-noticeable-difference-with-netflix-vmaf.html).

| Metric | Flag | Default | Meaning (threshold T) |
|---|---|---|---|
| Harmonic mean | `--vmaf-hmean` | **93** | Penalizes local dips; **default gate** because it cannot under-deliver: hmean ≥ T mathematically implies mean ≥ T |
| Arithmetic mean | `--vmaf-mean` | disabled | Average quality. Redundant as a gate whenever hmean is enabled at the same value — kept for reporting and for external comparability (Netflix 93 convention, all published ladders use arithmetic mean) |
| Median | `--vmaf-median` | disabled | ≥ 50% of frames at or above T |
| Percentile 25 | `--vmaf-p25` | disabled | ≥ 75% of frames at or above T |
| Percentile 10 | `--vmaf-p10` | disabled | ≥ 90% of frames at or above T |
| Percentile 5 | `--vmaf-p5` | disabled | ≥ 95% of frames at or above T |
| Percentile 1 | `--vmaf-p1` | disabled | ≥ 99% of frames at or above T |
| Min | `--vmaf-min` | disabled | 100% of frames at or above T — strictest floor. Can over-fire on transient frames (title cards, flash frames) and inflate bitrate |

**Mathematically guaranteed relationships:** `min ≤ p1 ≤ p5 ≤ p10 ≤ p25`; `min ≤ hmean ≤ mean`. Beyond these, the position of hmean relative to the percentiles depends on the frame distribution: in practice, for realistic per-segment VMAF scores, hmean usually sits between the low percentiles and the mean.

Consequences used everywhere below:
- **Gate composition:** one strict measure > several modest ones. Enabling `--vmaf-hmean T` makes `--vmaf-mean T` a tautology — don't gate both at the same value.
- **Diagnostic inversion:** the **mean − hmean gap** is the signal for "uniformly good" (small gap → QP is well tuned) vs "good on average with bad patches" (large gap → per-scene splitter cut, or QP granularity issue, or move the gate to hmean/min). The mean is always logged for this reason even when it's not gated.

**Rules:**
- All enabled thresholds must pass simultaneously (AND logic)
- Set any threshold to `-1` to disable it — remember that the harmonic mean gate is enabled by default, so pass `--vmaf-hmean -1` to silence it when you test other gates in isolation

> 💡 **Tip:** Start with `--vmaf-mean 93` alone (with `--vmaf-hmean -1` to disable the default gate) plus the `--debug` flag to inspect each encode attempt's VMAF score and identify problematic scenes, then move the actual gate to `--vmaf-hmean 93`, and add `--vmaf-p5` or `--vmaf-p1` only if a profile demands explicit percentile guarantees. Running mean + hmean gates at the same value is redundant — only one of them is real work.

### Recommended Values

| Use Case | Gate | Target value |
|---|---|---|
| "I am afraid of deleting my lossless master file" | `--vmaf-mean` + `--vmaf-min` | `99` + `93` ¹ |
| Archival / mastering | `--vmaf-hmean` | `95` |
| General streaming, VOD and live sports | `--vmaf-hmean` | `93` (default) ² |
| Mobile / bandwidth-constrained | `--vmaf-mean` | `85–90` ¹ |
| Quality consistency critical | `--vmaf-hmean` or `--vmaf-p5` | `90` |

¹ Pass `--vmaf-hmean -1` to disable the default gate when the profile does not rely on it.

² fast-motion content needs no dedicated profile: motion-heavy segments fail the gate and converge to a lower QP automatically.

**Why hmean everywhere except mobile:** hmean ≥ T implies mean ≥ T, so an hmean gate is strictly stronger than the classic mean 93 contract for no ambiguity cost — it simply also refuses segments with local dips. The mobile row keeps `--vmaf-mean 85–90` deliberately: on the cheap rung, average-level maximization *is* the contract, and an hmean gate would inflate bandwidth without any perceptible benefit at that distance/tier.

> **93 vs 95?** The 93 target comes from Rassool (RealNetworks, IEEE BMSB 2017, [PDF](https://realnetworks.com/sites/default/files/vmaf_reproducibility_ieee.pdf)), who found that encoding to ~93 would serve the vast majority of viewers with content *"either indistinguishable from original or with noticeable but not annoying distortion"* (MOS 4–5, tested on 4K clips). The 95 target comes from [Kah et al. (SPIE ADIP XLIV, 2021)](https://spie.org/Publications/Proceedings/Volume/11842): VMAF 95 is the lowest score *"at which a video signal is on average subjectively indistinguishable from the original video signal"* (ITU-R BT.500 subjective tests on a 4K OLED TV) — a deliberately higher bar. In Ozer's test on the 1080p *Meridian* clip, choosing 95 over 93 cost ~1400 kbps extra on the top rung (clip-specific, not universal). With `--vmaf-hmean 93` as default you get the 93 average backed by a no-bad-shots guarantee; the jump to 95 remains an explicit opt-in.

## Encoders

sptenc supports multiple HEVC and AV1 encoders. The `--encoder` flag (alias `-e`) selects which one to use.

| Encoder | Codec | Type | Platforms |
|---|---|---|---|
| `libx265` | HEVC | CPU | All |
| `hevc_nvenc` | HEVC | NVIDIA GPU | All (NVIDIA GPU required) |
| `hevc_vaapi` | HEVC | VAAPI GPU | Linux |
| `hevc_d3d12va` | HEVC | D3D12VA GPU | Windows |
| `hevc_videotoolbox` | HEVC | VideoToolbox GPU | macOS (Apple Silicon) |
| `libsvtav1` | AV1 | CPU | All |
| `av1_nvenc` | AV1 | NVIDIA GPU | All |
| `av1_vaapi` | AV1 | VAAPI GPU | Linux |

> **Note:** `libaom-av1` is not supported. It is too slow for sptenc's iterative per-segment QP search, where each segment may be encoded multiple times. `libsvtav1` is the only viable CPU AV1 encoder for this workflow. Run `sptenc check` to see which encoders your ffmpeg build supports.

> **Untested encoders:** `av1_vaapi` and `hevc_d3d12va` are implemented but have not been validated end to end (`hevc_vaapi` was, on Linux with an Intel iGPU). `hevc_d3d12va` targets Intel and AMD GPUs on Windows, which the author does not have: NVIDIA users should use `hevc_nvenc` and `av1_nvenc`, not D3D12VA. Feedback from Intel or AMD hardware is welcome.

### Encoder selection vs file size

| | CPU encoders (`libx265`, `libsvtav1`) | GPU encoders (`*_nvenc`, `*_vaapi`) |
|---|---|---|
| Output file size | ✅ Smallest | ❌ Larger: 1.4× on the episode measured below |
| Speed | Hours | ✅ Faster than realtime at 1080p, with several segments searched in parallel (`-C`, see below) |
| Recommended for | Final archival encode | VMAF profile prototyping, split threshold value search, and final encodes when time matters more than the last third of the size |

Same 26 min 1080p Blu-ray remux episode, same threshold (163 segments), cold cache, VMAF harmonic mean 93, on an RTX 5090 with a 16 cores / 32 threads CPU and `--vmaf-cuda`.:

| | `hevc_nvenc`, `-C 6` | `libx265` preset slow, `-C 3` |
|---|---|---|
| QP search | 10m40s | 52m42s |
| Video stream | 175.7 MiB | 124.9 MiB |
| Attempts per segment | 3.79 | 3.88 |

One episode is one data point: the size ratio depends on the content, and the times on the machine. The GPU search was bound by the CPU, which decodes the lossless FFV1 intermediate twice per attempt (once for the encode, once as the VMAF reference), not by the GPU (NVENC engines at 18%, CUDA at 25%): a smaller GPU with the same CPU would be close, the same GPU with a smaller CPU would not.

> **Concurrent segments (`-C`)**: segments are searched one at a time by default. The output is the same whatever the value, only the time it takes changes. <!-- libvmaf_cuda non-determinism: revert to the plain claim when --vmaf-cuda is removed (VMAF v1 has no CUDA kernels, v0.2.0) -->The one exception is `--vmaf-cuda`: `libvmaf_cuda` scores move in the last decimals between runs, so a segment sitting exactly on a threshold can be kept one QP apart by two runs of the same search (observed once in 163 segments on a 26 min episode, 0.1 MiB on the file).
>
> **With a GPU encoder**, the number of encoding engines on the card is not the limit: a worker only feeds the encoder while it encodes and waits for VMAF the rest of the time, and the frames come from the CPU decoding the FFV1 intermediate, as measured above. The same search on the RTX 5090 (three NVENC engines) took 14m7s with `-C 3` and 10m40s with `-C 6`, the CPU going from 52% to 90%: raise it until the CPU is saturated, the driver's encode session limit being the hard stop (an encode then fails to open its session).
>
> **With a CPU encoder**, a single encode already uses every thread of the machine, but does not keep a many-core CPU fully busy: on a 16 cores / 32 threads CPU, `libx265` at 1080p encoded 26% more frames per second with 2 concurrent segments (VMAF computed on CPU), and up to 46% more with 3 and `--vmaf-cuda`, which takes VMAF away from the CPU. Expect less with fewer cores or bigger pictures (+21% with 2 encodes at 2160p), mind the memory with 4K content, and measure on your machine.
>
> Segments started together can not learn from each other, so a few more attempts are needed at the beginning of a run: a cost only visible on short inputs. For the fastest workflow, `batchsearch --final-encode` searches the scene threshold on GPU then encodes the final file with the CPU equivalent: `-C` then only applies to the search, the final encode has its own `--final-concurrent-segments` as they do not run on the same hardware.

### GPU selection flags

When using a GPU encoder, you can target a specific device:

| Flag | Default | Used with |
|---|---|---|
| `--nvidia-gpu-index` | `0` | `hevc_nvenc`, `av1_nvenc` |
| `--vaapi-renderer-path` | `/dev/dri/renderD128` | `hevc_vaapi`, `av1_vaapi` |
| `--d3d12va-gpu-index` | `0` | `hevc_d3d12va` |

> These flags select the GPU device for **encoding**, and for **decoding** as well: a hardware encoder decodes with the same device. For hardware-accelerated decoding with a CPU encoder, or during `master`, `split`, `thresholds` or `vmaf`, use `--nvdec`, `--vaapi-dec`, `--d3d12va-dec`, or `--videotoolbox-dec`; the device flags above apply to them. A decode flag contradicting the hardware encoder (or NVDEC implied by `--vmaf-cuda`) is rejected.

## Adaptive QP Search

sptenc's per-segment QP search uses a 3-step algorithm that converges on the highest valid QP (smallest file) efficiently, even on the first run:

1. **Smart start** — The first candidate is the weighted mean QP from previous runs. On a cold start, it falls back to the midpoint of the encoder's QP range (e.g. QP 26 for libx265's 0–51 range).
2. **Bracketing** — Steps from the starting point in increment-sized steps to find one valid QP (passes VMAF) and one invalid QP (fails VMAF), closing the search range around the boundary. With cached stats the step size is the observed standard deviation; on a cold start it falls back to a quarter of the QP range (e.g. 13 for libx265's 0–51 range).
3. **Interpolation** — Once bracketed, **Fritsch-Butland monotone cubic interpolation** predicts the optimal candidate within the range, walking toward the highest valid QP without blind probing.

This typically requires only 3–5 encode attempts per segment, compared to a brute-force search that could probe dozens of QP values.

### Persistent stats from previous runs

After each encode job finishes, sptenc stores QP statistics **per encoder and VMAF profile** (i.e. the combination of encoder, enabled metrics, and their target values), weighted by the number of segments. A `batchsearch` run only stores its winning candidate (plus the final CPU encode with `--final-encode`, on that encoder's own cache): the other candidates are the same content encoded again and would over-represent that file. Within the run though, every candidate starts from the QPs the previous candidates found: the same content is the best prior there is, so only the first candidate pays the learning cost.

What is at stake is the number of encodes per segment. On the episode of the [Encoders](#encoder-selection-vs-file-size) section, a cold cache cost 3.9 attempts per segment with `libx265` and 3.8 with `hevc_nvenc`, the run's own ephemeral statistics doing the learning from the first segments on. A segment can not take fewer than two, the QP kept and the next one failing, so what a warm cache can save is bounded by that: it has not been measured on that content yet.

The cache provides:
- **Mean QP** — a better-informed starting point than the encoder midpoint
- **Standard deviation** — a tuned step size for bracketing, rather than a heuristic fraction of the range

The stats files are **profile-specific**: changing the encoder or any VMAF threshold value starts a fresh learning curve.

### Cache isolation with profiles

Because a given VMAF target can require very different QP distributions depending on the source (e.g. clean animation vs. grainy film), mixing them into the same cache effectively poisons it. Use `--cache-profile <name>` (e.g. `pixar_animation`, `grainy_90s`) to keep these histories separate. Without a profile, all runs with the same encoder and VMAF profile share the same cache.

| Flag | Short | Default | Description |
|---|---|---|---|
| `--stats-cache-dir` | `-s` | OS cache dir (`~/.cache/sptenc` or equivalent) | Directory where QP statistics are stored |
| `--cache-profile` | `-c` | *(none)* | Isolate cache history between content types |

## Base ffmpeg encode options

These are the opinionated defaults sptenc passes to ffmpeg. They are intentionally not configurable: the goal is to let you tune **VMAF thresholds** and **scene detection**, not encoder minutiae. If you need full control over every ffmpeg flag, ffmpeg itself is the right tool.

The defaults are selected for a single goal: **a VMAF target met by every segment, at the smallest file size**. Every option is chosen with that trade-off in mind.

> **10-bit output is mandatory.** All encoders target `yuv420p10le` (`main10` for HEVC, `main` for AV1 which includes 10-bit). 10-bit greatly reduces banding and improves compression efficiency at low bitrates — it is the modern baseline for quality encoding.
>
> The lossless FFV1 master is already stored in that format. It is bit-exact with 8-bit and 10-bit 4:2:0 sources, by far the most common ones. 4:2:2 and 4:4:4 sources get their chroma subsampled at that step (luma stays exact, and luma is all VMAF measures), and sources deeper than 10 bits are reduced to 10 bits.

Under the hood, here are the base options used by sptenc. `X` is the QP value being tested for the current segment.

### HEVC

**libx265**
```bash
ffmpeg [...] -c:v 'libx265' -profile:v 'main10' -pix_fmt 'yuv420p10le' -preset 'slow' -qp 'X' [...]
```

**hevc_nvenc**
```bash
ffmpeg [...] -c:v 'hevc_nvenc' -profile:v 'main10' -preset 'p7' -tune 'hq' -rc 'constqp' -qp 'X' -rc-lookahead 32 -spatial-aq 1 -temporal-aq 1 [...]
```

**hevc_vaapi**
```bash
ffmpeg [...] -c:v 'hevc_vaapi' -profile:v 'main10' -rc_mode 'CQP' -qp 'X' [...]
```

**hevc_d3d12va**
```bash
ffmpeg [...] -c:v 'hevc_d3d12va' -profile:v 'main10' -rc_mode 'CQP' -qp 'X' [...]
```

**hevc_videotoolbox**
```bash
ffmpeg [...] -c:v 'hevc_videotoolbox' -profile:v 'main10' -q:v 'X' -bsf:v 'dump_extra' [...]
```

### AV1

**libsvtav1**
```bash
ffmpeg [...] -c:v 'libsvtav1' -pix_fmt 'yuv420p10le' -preset '3' -qp 'X' [...]
```

**av1_nvenc**
```bash
ffmpeg [...] -c:v 'av1_nvenc' -preset 'p7' -tune 'hq' -rc 'constqp' -qp 'X' -rc-lookahead 32 -spatial-aq 1 -temporal-aq 1 [...]
```

**av1_vaapi**
```bash
ffmpeg [...] -c:v 'av1_vaapi' -profile:v 'main' -rc_mode 'CQP' -global_quality 'X' [...]
```

## Output

The output is always Matroska (`.mkv`) because it is the most permissive container for stream copy.

Color metadata (`color_range`, `colorspace`, `color_trc` and `color_primaries`) is probed from the source and re-injected into the output container. HDR metadata handling is still being validated.

You specify the output path explicitly as the final positional argument for file-producing commands (`encode`, `batchsearch`, `remux`, `master`, `concat`). Directory-producing commands (`split`) take an output directory in the same way.

### Metadata tags

The output file contains the following metadata tags on the video stream:

- `sptenc_url` and `sptenc_version` — tool provenance
- `sptenc_encoder` and `sptenc_encoder_preset` — encoder used
- `sptenc_segments_count` — number of segments
- `sptenc_stats_min_qp`, `sptenc_stats_max_qp`, `sptenc_stats_weighted_qp` — QP statistics
- `sptenc_vmaf_model` — VMAF model used
- `sptenc_vmaf_conf_*` — all enabled VMAF threshold values
- `sptenc_vmaf_result_*` — final VMAF scores (min, p1, p5, p10, p25, median, hmean, mean, max)
- `sptenc_best_effort_segments` — number of segments that stopped at minimum QP without reaching the target VMAF profile (omitted if zero)

## Installation

Build from source:

```bash
git clone https://github.com/hekmon/sptenc.git
cd sptenc
go build -o sptenc ./cmd/sptenc/
```

**External Dependencies:**
- `ffmpeg` - the one your distribution, Homebrew or the [static builds](https://ffmpeg.org/download.html) ship is enough: it must be built with `libvmaf` and with `libx265` (or another supported encoder), which the usual packages are. A recent version is highly recommended. Nothing to compile, with one exception: `--vmaf-cuda` needs `libvmaf_cuda`, which no package ships. See the [build guide](https://gist.github.com/hekmon/b273e55139183370c5000f766fccc128) for that one (works in WSL on Windows).
- `ffprobe` - bundled with ffmpeg
- `mkvpropedit` - from [MKVToolNix](https://mkvtoolnix.download/)

**Where the binaries are looked for:**

| Binary | Linux / macOS | Windows | Override (global flag) |
|---|---|---|---|
| `ffmpeg` | `PATH` | `.\ffmpeg.exe` (current directory, not `PATH`) | `--ffmpeg-path` |
| `ffprobe` | `PATH` | `.\ffprobe.exe` (current directory, not `PATH`) | `--ffprobe-path` |
| `mkvpropedit` | `PATH` | `C:\Program Files\MKVToolNix\mkvpropedit.exe` | `--mkvpropedit-path` |

Example: `sptenc --ffmpeg-path /opt/ffmpeg/bin/ffmpeg encode [...]`. Run `sptenc check` to verify everything is found and usable.

## License

MIT. See [LICENSE](LICENSE).
