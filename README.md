# Split Encoder

`sptenc` (Split Encoder) is a scene-aware, [VMAF](https://github.com/Netflix/vmaf)-driven video transcoder for enthusiasts who want perceptually transparent encodes at the smallest possible file size, and are willing to trade encoding speed for guaranteed quality.

It splits the input into scene-aligned segments, encodes each one independently, and validates the result against configurable VMAF thresholds before accepting it.
Failed segments are automatically re-encoded at a lower QP until all thresholds are met.
A final, complete VMAF comparison between the encoded output and original source is performed at the end, and its results are embedded into the output file's metadata tags.

This approach produces the smallest possible file without compromising the target quality defined by the VMAF profile.

> **Trade-off:** Achieving both smaller file size AND guaranteed quality comes at a cost: encoding time will be significantly longer than standard single-pass encoding, as multiple QP values are tested on each segment until all VMAF thresholds are met. This is not the same as a single CRF pass with a whole-file VMAF check — that approach only validates an average, so one complex scene in an otherwise steady movie can be destroyed while the overall result still looks acceptable. sptenc enforces its quality floor on every single scene independently.

> **Inspiration:** sptenc is inspired by Netflix's [Dynamic Optimizer](https://netflixtechblog.com/dynamic-optimizer-a-perceptual-video-encoding-optimization-framework-e19f1e3a277f) framework, which pioneered scene-aware, perceptually-optimized video encoding, and the VMAF perceptual quality models that power it. This project is built for power users encoding on their own hardware, not for streaming-scale infrastructure.

## Who is this for?

You probably don't need sptenc if you just want to shrink a video for your phone. Standard tools like HandBrake or ffmpeg with CRF are faster and perfectly fine for that.

sptenc is built for workflows where you want the **smallest file size that still meets a provable quality floor**:

- **Archival & preservation** — You have a high-bitrate source or lossless master (or an expensive AI-upscaled restoration) and want to compress it without ever dropping below a perceptual quality floor you can prove.
- **Quality-per-bit optimization** — You target specific visual fidelity at the smallest possible size and currently do manual CRF sweeps, screenshot comparisons, or test encodes to find the right settings. sptenc automates that search and produces a VMAF report proving the result.
- **Large collection processing** — You process many files against a single, carefully tuned quality profile. sptenc treats that profile as a contract: every segment is encoded, measured, and corrected until it passes, with no manual verification required.
- **NAS / media server optimization** — You maintain a personal library of high-bitrate remuxes and need to balance quality against storage costs. sptenc replaces manual CRF trial-and-error with a measurable guarantee, so you keep the quality that matters and reclaim the space that doesn't.

If you already know why CRF averages can hide bad frames, sptenc closes the loop: encode, measure, correct, converge.

## Key Features

- 🎯 **VMAF-driven encoding** - Guarantees a minimum perceptual quality level, not just a CRF or bitrate target
- 🎬 **Scene-aware segmentation** - Segments aligned with scene cuts for consistent quality
- 📊 **Multi-metric VMAF validation** - Combine mean, harmonic mean, median, percentiles (P1/P5/P10/P25), and worst-frame thresholds simultaneously; all must pass (AND logic)
- 🔍 **4 VMAF models** - 1080p or 4K model auto-selected based on input resolution; NEG variants available via `--vmaf-neg` for upscaled/denoised/sharpened sources (recommended)
- 📋 **VMAF report embedded in output** - Final VMAF comparison results stored in the output file's metadata tags for full traceability
- 🧠 **Adaptive QP search with persistent stats** - Learns from previous encodes to reduce QP search iterations for future encodings (see below)
- ⚡ **Multi-encoder support** - HEVC (`libx265`, `hevc_nvenc`, `hevc_vaapi`, `hevc_d3d12va`, `hevc_videotoolbox`) and AV1 (`svtav1`, `av1_nvenc`, `av1_vaapi`). Use GPU encoders for fast VMAF profile prototyping, CPU encoders for the smallest final file size.
- 🖥️ **VMAF-CUDA** - Optional CUDA-accelerated VMAF computation (requires libvmaf with CUDA support) with the `--vmaf-cuda` flag. NVDEC hardware decoding is automatically enabled alongside it when the source codec is compatible.
- 🚀 **Optional concurrent segment encoding** - GPU encoders can search multiple segments in parallel via `--concurrent-segments` (`-C`), significantly reducing total runtime for threshold discovery and large batch jobs. CPU encoders are locked to sequential encoding to prevent cache thrashing.
- 🎵 **Automatic FLAC compression** - If all audio tracks are PCM, they are losslessly re-encoded to FLAC during remux to reduce file size without quality loss
- 🎨 **Container color metadata preservation** - `color_range`, `colorspace`, `color_trc`, and `color_primaries` are probed from the source and re-injected into the output container (HDR metadata handling is still being validated)
- 🔬 **Automatic threshold search** - `batchsearch` tests multiple scene detection thresholds to find the one that produces the smallest file while still meeting your VMAF targets

## How It Works

1. **Scene detection** - FFmpeg `scdet` analyzes the video to find scene boundaries, producing semantically coherent segments. For precise frame-accurate cuts, a lossless FFV1 master is used: every frame is self-contained, so splits can happen at any frame without quality loss or dropped frames.
2. **Per-segment encoding** - Each segment is encoded independently with the chosen encoder (e.g. `libx265`, `hevc_nvenc`, `svtav1`).
3. **VMAF validation (post-encode)** - After encoding, each segment's VMAF scores are computed and checked against all configured thresholds. Any failure triggers a re-encode at a lower QP.
4. **Adaptive QP search** - Each segment starts from a smart QP estimate, brackets the valid range with stepped probes, then uses interpolation to converge on the highest valid QP (smallest file) in just a few attempts. Persistent stats from previous runs further accelerate this (see below).
5. **Best effort** - If the encoder's minimum QP is reached and thresholds are still not met (e.g. pathological scene), the segment is accepted and flagged as "best effort" in logs.
6. **Muxing & tagging** - Segments are merged into a single output file. Audio, subtitles, and other streams from the original source are remuxed into the final file. PCM audio tracks are automatically losslessly compressed to FLAC. A final VMAF comparison between the complete encoded file and original source is performed, with results displayed in logs and embedded in the output file's metadata tags. Matroska statistics tags are regenerated for full player compatibility.

## Why QP instead of CRF?

sptenc controls quality with **QP (Quantization Parameter)** in **CQP (Constant QP)** mode, not CRF.

- **CQP** applies the same base quantization to every frame. The encoder does not second-guess the quality target — QP 22 means QP 22, period. This makes the relationship between the dial and VMAF **stable and predictable**, which is what allows the interpolation search to converge in 3–5 attempts instead of testing every value.
- **CRF** (Constant Rate Factor) tells the encoder to vary QP frame-by-frame internally to hit a perceptual quality target. The same CRF value can produce different effective quantizations depending on scene complexity, which turns the search space into a moving target. Interpolating across CRF values is noisy and unreliable.

The encoder still applies local optimizations (adaptive quantization, lookahead), but since these are held **constant across every tested QP**, the comparison between candidates remains stable.

The trade-off is familiar: CRF produces smaller files for a given *average* quality, but it delegates quality control to the encoder. sptenc takes the opposite approach — it fixes quantization and lets the scene splitter decide where quality should vary. This is slower, but it makes the VMAF guarantee enforceable segment by segment.

## Commands

sptenc is organized into subcommands. Run `sptenc <command> --help` for detailed usage of each.

| Command | Alias | Category | Purpose |
|---|---|---|---|
| `encode` | `e` | Main | Full pipeline: split (if needed), encode segments, validate VMAF, remux, and tag |
| `verify` | `v` | Main | Verify that ffmpeg, ffprobe, and mkvpropedit are present and list available encoders |
| `master` | `m` | Tooling | Create a lossless FFV1 intermediate from a source file for frame-accurate splitting |
| `split` | `s` | Tooling | Detect scene changes and split a video into separate segment files |
| `concat` | `c` | Tooling | Concatenate video files from a directory into a single file without re-encoding |
| `remux` | `r` | Tooling | Replace the video track of a file with a new one without re-encoding |
| `vmaf` | | Tooling | Compute VMAF between a reference and a distorted video |
| `batchsearch` | `bs` | Advanced | Search for the scene threshold that yields the smallest passing file by encoding multiple candidates |

## Input Requirements

### Constant Frame Rate (CFR)
**Variable frame rate (VFR) content is not supported.** VMAF requires frame-exact alignment between reference and distorted videos. VFR content causes FFmpeg to duplicate or drop frames when forced to a constant rate, invalidating VMAF scores. sptenc rejects VFR inputs at startup.

### Pre-segmented Input (Optional)

Instead of letting sptenc split the input automatically, you can provide an already-split directory of segments. Files must be `.mkv` or `.mp4` and are processed in **alphabetical order** — name them accordingly (e.g. `seg_01.mkv`, `seg_02.mkv`) to preserve scene order. All segments must share the same codec and frame rate.

```bash
./sptenc encode ./gop_dir/ output.mkv --original-file original_with_audio.mkv
```

When using a pre-segmented directory, `--original-file` (alias `-f`) is **required** so sptenc can remux audio, subtitles, and other streams into the final output.

## Quick Start

### Verify your environment
```bash
./sptenc verify
```

### Basic encode - VMAF harmonic mean ≥ 93 (default)
```bash
./sptenc encode video.mkv output.mkv
```

### Strict quality with multiple thresholds
```bash
./sptenc encode video.mkv output.mkv --vmaf-mean 95 --vmaf-p5 85 --vmaf-min 70
```

### Upscaled or denoised source - use VMAF NEG
```bash
./sptenc encode upscaled.mkv output.mkv --vmaf-neg --vmaf-mean 93
```

### Fast VMAF profile prototyping with NVENC on the second GPU
```bash
./sptenc encode video.mkv output.mkv --encoder hevc_nvenc --vmaf-cuda --nvidia-gpu-index 1 --vmaf-mean 93
# Once happy with the profile, re-run with the default libx265 for the final smaller encode
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
./sptenc batchsearch video.mkv output.mkv --encoder hevc_nvenc --vmaf-cuda --vmaf-hmean 93
# Once the best threshold is found, run a final CPU encode to shrink even further the file
./sptenc batchsearch video.mkv output.mkv --encoder hevc_nvenc --vmaf-cuda --vmaf-hmean 93 --final-encode
```

### Manual pipeline (master → split → encode)
```bash
./sptenc master video.mkv master.mkv
./sptenc split master.mkv ./segments/ --master --threshold 12
./sptenc encode ./segments/ output.mkv --original-file video.mkv
```

> Use the `thresholds` command to preview candidate thresholds and their scene distributions without encoding. Experiment with `--min-threshold` (1–100, default 14): higher values detect fewer scenes, lower values detect more.
>
> Both `master`, `split`, and `vmaf` support hardware-accelerated decoding via `--nvdec`, `--vaapi-dec`, `--d3d12va-dec`, or `--videotoolbox-dec` to speed up processing.

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

The `--min-segment-length` flag (default 5s) removes boundaries that would create segments shorter than the given duration. This is a quality-floor guardrail: it prevents unreliable percentile metrics and B/P-frame starvation by merging short segments into their shorter neighbour.

It does **not** protect against the opposite problem. Segments longer than ~5 seconds may still be too long for your tolerance of the drowning risk. That judgment remains yours.

Use the `thresholds` command to preview the segment distributions a threshold would produce before committing to an `encode` or a `batchsearch` run. It is fast and produces no files.

## VMAF

### Models

| Resolution | Standard Model | NEG Model |
|---|---|---|
| < 4K | `vmaf_v0.6.1` | `vmaf_v0.6.1neg` |
| ≥ 4K (2160p) | `vmaf_4k_v0.6.1` | `vmaf_4k_v0.6.1neg` |

The model is **automatically selected** based on input resolution. Use `--vmaf-neg` when the source has been upscaled, sharpened, or denoised: NEG models are designed so that enhancement-based processing (sharpening, upscaling filters) does not inflate the score, whereas standard models can over-score such content.

> **Note:** NEG stands for **No Enhancement Gain**. The standard `vmaf_v0.6.1` model predicts the viewing condition of a **1080p HDTV at 3 picture heights**, and `vmaf_4k_v0.6.1` that of a **4K TV at 1.5 picture heights** — keep this in mind when interpreting scores for other display formats. See the [VMAF documentation](https://github.com/Netflix/vmaf/blob/master/resource/doc/models.md#disabling-enhancement-gain-neg-mode) for details.

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
| `svtav1` | AV1 | CPU | All |
| `av1_nvenc` | AV1 | NVIDIA GPU | All |
| `av1_vaapi` | AV1 | VAAPI GPU | Linux |

> **Note:** `libaom-av1` is not supported. It is too slow for sptenc's iterative per-segment QP search, where each segment may be encoded multiple times. `svtav1` is the only viable CPU AV1 encoder for this workflow. Run `sptenc verify` to see which encoders your ffmpeg build supports.

### Encoder selection vs file size

| | CPU encoders (`libx265`, `svtav1`) | GPU encoders (`*_nvenc`, `*_vaapi`) |
|---|---|---|
| Output file size | ✅ Optimal | ❌ ~1.5–2× larger |
| Speed | Slower | ✅ Much faster |
| Recommended for | Final archival encode | VMAF profile prototyping, split threshold value search |

> **CPU encoders are sequential by design.** A single `libx265` or `svtav1` session already saturates physical CPU cores. Hyperthreading/SMT does not double throughput for heavy encode workloads, so running multiple instances just thrashes cache and hurts total throughput. `--concurrent-segments` is hard-limited to `1` for CPU encoders; use a GPU encoder with `-C` if you need speed, or `batchsearch --final-encode` to discover thresholds quickly on GPU and automatically re-encode with the CPU equivalent for the smallest file.

### GPU selection flags

When using a GPU encoder, you can target a specific device:

| Flag | Default | Used with |
|---|---|---|
| `--nvidia-gpu-index` | `0` | `hevc_nvenc`, `av1_nvenc` |
| `--vaapi-renderer-path` | `/dev/dri/renderD128` | `hevc_vaapi`, `av1_vaapi` |
| `--d3d12va-gpu-index` | `0` | `hevc_d3d12va` |

> These flags select the GPU device for **encoding**. For hardware-accelerated **decoding** during `master`, `split`, or `vmaf`, use `--nvdec`, `--vaapi-dec`, `--d3d12va-dec`, or `--videotoolbox-dec` instead. The corresponding GPU selection flags (`--nvidia-gpu-index`, `--vaapi-renderer-path`, `--d3d12va-gpu-index`) also apply when decoding.

## Adaptive QP Search

sptenc's per-segment QP search uses a 3-step algorithm that converges on the highest valid QP (smallest file) efficiently, even on the first run:

1. **Smart start** — The first candidate is the weighted mean QP from previous runs. On a cold start, it falls back to the midpoint of the encoder's QP range (e.g. QP 26 for libx265's 0–51 range).
2. **Bracketing** — Steps from the starting point in increment-sized steps to find one valid QP (passes VMAF) and one invalid QP (fails VMAF), closing the search range around the boundary. With cached stats the step size is the observed standard deviation; on a cold start it falls back to half the QP range midpoint.
3. **Interpolation** — Once bracketed, **Fritsch-Butland monotone cubic interpolation** predicts the optimal candidate within the range, walking toward the highest valid QP without blind probing.

This typically requires only 3–5 encode attempts per segment, compared to a brute-force search that could probe dozens of QP values.

### Persistent stats from previous runs

After each encode job finishes, sptenc stores QP statistics **per encoder and VMAF profile** (i.e. the combination of encoder, enabled metrics, and their target values), weighted by the number of segments. This further accelerates convergence on subsequent runs:

| Encode job | Cold start | With cached stats |
|---|---|---|
| Small episode | ~7h30 | ~4h |
| Film | ~85h | ~60h |

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

The defaults are selected for a single goal: **guaranteed perceptual quality at the smallest possible file size**. Every option is chosen with that trade-off in mind.

> **10-bit output is mandatory.** All encoders target `yuv420p10le` (`main10` for HEVC, `main` for AV1 which includes 10-bit). 10-bit greatly reduces banding and improves compression efficiency at low bitrates — it is the modern baseline for quality encoding.

Under the hood, here are the base options used by sptenc. `X` is the QP value being tested for the current segment.

### HEVC

**libx265**
```bash
ffmpeg [...] -c:v 'libx265' -profile:v 'main10' -pix_fmt 'yuv420p10le' -preset 'slow' -qp 'X' -x265-params 'aq-mode=3' [...]
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
ffmpeg [...] -c:v 'hevc_videotoolbox' -profile:v 'main10' -q:v 'X' [...]
```

### AV1

**svtav1**
```bash
ffmpeg [...] -c:v 'libsvtav1' -pix_fmt 'yuv420p10le' -preset '6' -qp 'X' [...]
```

**av1_nvenc**
```bash
ffmpeg [...] -c:v 'av1_nvenc' -preset 'p4' -tune 'hq' -rc 'constqp' -qp 'X' -rc-lookahead 32 -spatial-aq 1 -temporal-aq 1 [...]
```

**av1_vaapi**
```bash
ffmpeg [...] -c:v 'av1_vaapi' -profile:v 'main' -rc_mode 'CQP' -global_quality 'X' [...]
```

## Output

The output is always Matroska (`.mkv`) because it is the most permissive container for stream copy.

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

## Installation

Build from source:

```bash
git clone https://github.com/hekmon/sptenc.git
cd sptenc
go build -o sptenc ./cli/
```

**External Dependencies:**
- `ffmpeg` - compiled with `libx265` (or another supported encoder) and `libvmaf` support ([build guide](https://gist.github.com/hekmon/b273e55139183370c5000f766fccc128)) - can be used in WSL to get `libvmaf_cuda` support on Windows. A recent ffmpeg version is highly recommended.
- `ffprobe` - bundled with ffmpeg build
- `mkvpropedit` - from [MKVToolNix](https://mkvtoolnix.download/)

## License

MIT. See [LICENSE](LICENSE).
