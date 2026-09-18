# Split Encoder

`sptenc` (Split Encoder) is a scene-aware, [VMAF](https://github.com/Netflix/vmaf)-driven video transcoder for enthusiasts who want perceptually transparent encodes at the smallest possible file size, and are willing to trade encoding speed for guaranteed quality.

It splits the input into scene-aligned segments, encodes each one independently, and validates the result against configurable VMAF thresholds before accepting it.
Failed segments are automatically re-encoded at a lower QP until all thresholds are met.
A final, complete VMAF comparison between the encoded output and original source is performed at the end, and its results are embedded into the output file's metadata tags.

This approach produces the smallest possible file without compromising the target quality defined by the VMAF profile.

> **Trade-off:** Achieving both smaller file size AND guaranteed quality comes at a cost: encoding time will be significantly longer than standard single-pass encoding, as multiple QP values are tested on each segment until all VMAF thresholds are met.

> **Inspiration:** sptenc is inspired by Netflix's [Dynamic Optimizer](https://netflixtechblog.com/dynamic-optimizer-a-perceptual-video-encoding-optimization-framework-e19f1e3a277f) framework, which pioneered scene-aware, perceptually-optimized video encoding, and the VMAF perceptual quality models that power it. This project is built for power users encoding on their own hardware, not for streaming-scale infrastructure.

## Who is this for?

You probably don't need sptenc if you just want to quickly shrink a video for your phone. Standard tools like HandBrake or ffmpeg with CRF are faster and perfectly fine for that.

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
- 🔍 **4 VMAF models, auto-selected** - Automatically uses 1080p or 4K model based on input resolution; add `--vmafneg` for NEG variants (recommended for upscaled/denoised/sharpened sources)
- 📋 **VMAF report embedded in output** - Final VMAF comparison results stored in the output file's metadata tags for full traceability
- 🧠 **Adaptive QP search with persistent stats** - Learns from previous encodes to reduce QP search iterations for future encodings (see below)
- ⚡ **Multi-encoder support** - HEVC (`libx265`, `hevc_nvenc`, `hevc_vaapi`, `hevc_d3d12va`, `hevc_videotoolbox`) and AV1 (`svtav1`, `av1_nvenc`, `av1_vaapi`). Use GPU encoders for fast VMAF profile prototyping, CPU encoders for the smallest final file size.
- 🖥️ **VMAF-CUDA** - Optional CUDA-accelerated VMAF computation (requires libvmaf with CUDA support) with the `--vmafcuda` flag. NVDEC hardware decoding is automatically enabled alongside it when the source codec is compatible.
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
| `batchsearch` | `bs` | Advanced | Automatically search for the optimal scene detection threshold by encoding multiple candidates |

## Input Requirements

### Constant Frame Rate (CFR)
**Variable frame rate (VFR) content is not supported.** VMAF requires frame-exact alignment between reference and distorted videos. VFR content causes FFmpeg to duplicate or drop frames when forced to a constant rate, invalidating VMAF scores. sptenc rejects VFR inputs at startup.

### Pre-segmented Input (Optional)

Instead of letting sptenc split the input automatically, you can provide an already-split directory of segments. Files must be `.mkv` or `.mp4` and are processed in **alphabetical order** — name them accordingly (e.g. `seg_01.mkv`, `seg_02.mkv`) to preserve scene order. All segments must share the same codec and frame rate.

```bash
./sptenc encode ./gop_dir/ --originalfile original_with_audio.mkv
```

When using a pre-segmented directory, `--originalfile` (alias `-f`) is **required** so sptenc can remux audio, subtitles, and other streams into the final output.

## Quick Start

### Verify your environment
```bash
./sptenc verify
```

### Basic encode - VMAF harmonic mean ≥ 93 (default)
```bash
./sptenc encode video.mkv
```

### Strict quality with multiple thresholds
```bash
./sptenc encode video.mkv --vmafmean 95 --vmafp5 85 --vmafmin 70
```

### Upscaled or denoised source - use VMAF NEG
```bash
./sptenc encode upscaled.mkv --vmafneg --vmafmean 93
```

### Fast VMAF profile prototyping with NVENC on the second GPU
```bash
./sptenc encode video.mkv --encoder hevc_nvenc --vmafcuda --nvidiagpuindex 1 --vmafmean 93
# Once happy with the profile, re-run with the default libx265 for the final smaller encode
```

### Pre-segmented directory
```bash
./sptenc encode ./gop_dir/ --originalfile original_with_audio.mkv --vmafmean 95
```

### Concatenate segments without re-encoding
```bash
./sptenc concat ./segments/ --outputdir ./merged/
```

### Find the optimal scene threshold automatically
```bash
./sptenc batchsearch video.mkv --encoder hevc_nvenc --vmafcuda --vmafhmean 93
# Once the optimal threshold is found, run the final CPU encode
./sptenc batchsearch video.mkv --encoder hevc_nvenc --vmafcuda --vmafhmean 93 --finalencode
```

### Manual pipeline (master → split → encode)
```bash
./sptenc master video.mkv
./sptenc split "video - ffv1 master.mkv" --master --threshold 12 --outputdir ./segments/
./sptenc encode ./segments/ --originalfile video.mkv
```

> Use `--analyze` with the `split` command to preview detected scenes without splitting. Experiment with `--threshold` (1–100, default 10): higher values detect fewer scenes, lower values detect more.
>
> Both `master` and `split` support hardware-accelerated decoding via `--nvdec`, `--vaapidec`, `--d3d12dec`, or `--videotoolboxdec` to speed up lossless master creation.

## VMAF

### Models

| Resolution | Standard Model | NEG Model |
|---|---|---|
| < 4K | `vmaf_v0.6.1` | `vmaf_v0.6.1neg` |
| ≥ 4K (2160p) | `vmaf_4k_v0.6.1` | `vmaf_4k_v0.6.1neg` |

The model is **automatically selected** based on input resolution. Use `--vmafneg` when the source has been upscaled, sharpened, or denoised — standard models will over-score such content.

> **Note:** NEG stands for **No Enhancement Gain**. These variants are designed to avoid over-scoring processed content (upscaled, denoised, sharpened). See the [VMAF documentation](https://github.com/Netflix/vmaf/blob/master/resource/doc/models.md#disabling-enhancement-gain-neg-mode) for details.

### Quality Score Reference

| VMAF Score | Perceptual Quality | Typical Context |
|---|---|---|
| **95–100** | Indistinguishable from source | Archival, mastering, very high bitrate |
| **90–95** | Noticeable but not annoying | Premium streaming - Netflix standard: **93** |
| **80–90** | Good quality, minor artifacts | Acceptable HD streaming |
| **70–80** | Medium quality, visible artifacts | SD streaming or constrained bitrate |
| **60–70** | Noticeable degradation | Low resolution or heavy compression |
| **40–60** | Poor quality | Aggressive encoding |
| **< 40** | Very poor quality | Very low resolutions (180p–240p) |

**Sources:** [StreamingLearningCenter](https://streaminglearningcenter.com/learning/mapping-ssim-vmaf-scores-subjective-ratings.html) · [StreamingMedia](https://www.streamingmedia.com/Articles/Columns/The-Producers-View/Comparing-Quality-Metrics-Up-and-Down-the-Encoding-Ladder-121764.aspx)

### sptenc thresholds

VMAF scores range from 0 to 100. A difference of **~6 points ≈ 1 JND**
(Just Noticeable Difference — detectable by 75% of viewers; 2 JND / 12 points detectable by ~90%). See [Netflix via StreamingLearningCenter — Just Noticeable Difference](https://streaminglearningcenter.com/codecs/finding-the-just-noticeable-difference-with-netflix-vmaf.html).

| Metric | Flag | Default | Meaning (threshold T) |
|---|---|---|---|
| Harmonic mean | `--vmafhmean` | **93** | Penalizes local dips; **default gate** because it cannot under-deliver: hmean ≥ T mathematically implies mean ≥ T |
| Arithmetic mean | `--vmafmean` | disabled | Average quality. Redundant as a gate whenever hmean or a low percentile is enabled — kept for reporting and for external comparability (Netflix 93 convention, all published ladders use arithmetic mean) |
| Median | `--vmafmedian` | disabled | ≥ 50% of frames at or above T |
| Percentile 25 | `--vmafp25` | disabled | ≥ 75% of frames at or above T |
| Percentile 10 | `--vmafp10` | disabled | ≥ 90% of frames at or above T |
| Percentile 5 | `--vmafp5` | disabled | ≥ 95% of frames at or above T |
| Percentile 1 | `--vmafp1` | disabled | ≥ 99% of frames at or above T |
| Min | `--vmafmin` | disabled | 100% of frames at or above T — strictest floor. Can over-fire on transient frames (title cards, flash frames) and inflate bitrate |

**Fixed relationship:** `min ≤ p1 ≤ p5 ≤ … ≤ hmean ≤ mean`

Consequences used everywhere below:
- **Gate composition:** one strict measure > several modest ones. Enabling `--vmafhmean T` makes `--vmafmean T` a tautology — don't gate both.
- **Diagnostic inversion:** the **mean − hmean gap** is the signal for "uniformly good" (small gap → QP is well tuned) vs "good on average with bad patches" (large gap → per-scene splitter cut, or QP granularity issue, or move the gate to hmean/min). The mean is always logged for this reason even when it's not gated.

**Rules:**
- All enabled thresholds must pass simultaneously (AND logic)
- Set any threshold to `-1` to disable it

> 💡 **Tip:** Start with `--vmafmean 93` alone with the `--debug` flag to inspect each encode attempt's VMAF score and identify problematic scenes, then move the actual gate to `--vmafhmean 93`, and add `--vmafp5` or `--vmafp1` only if a profile demands explicit percentile guarantees. Running mean + hmean gates at the same value is redundant — only one of them is real work.

### Recommended Values

| Use Case | Gate | Target value |
|---|---|---|
| "I am afraid of deleting my lossless master file" | `--vmafmean` + `--vmafmin` | `99` + `93` ¹ |
| Archival / mastering | `--vmafhmean` | `95` |
| General streaming / VOD | `--vmafhmean` | `93` (default) |
| Live sports / fast motion | `--vmafhmean` | `93` |
| Mobile / bandwidth-constrained | `--vmafmean` | `85–90` ¹ |
| Quality consistency critical | `--vmafhmean` or `--vmafp5` | `90` |

¹ Pass `--vmafhmean -1` to disable the default gate when the profile does not rely on it.

**Why hmean everywhere except mobile:** hmean ≥ T implies mean ≥ T, so an hmean gate is strictly stronger than the classic mean 93 contract for no ambiguity cost — it simply also refuses segments with local dips. The mobile row keeps `--vmafmean 85–90` deliberately: on the cheap rung, average-level maximization *is* the contract, and an hmean gate would inflate bandwidth without any perceptible benefit at that distance/tier.

> **93 vs 95?** The 93 target comes from a RealNetworks white paper showing it delivers content that is *"indistinguishable from original or with noticeable but not annoying distortion"* for most viewers ([StreamingLearningCenter — analysis](https://streaminglearningcenter.com/encoding/optimal_encoding_ladder_vmaf.html)). The 95 target, from a more recent paper, is the lowest score at which content is *"on average subjectively indistinguishable from the original"* — a higher bar that costs ~1400 kbps extra at the top rung. With `--vmafhmean 93` as default you get the 93 average backed by a no-bad-shots guarantee; the jump to 95 remains an explicit opt-in.

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

### GPU selection flags

When using a GPU encoder, you can target a specific device:

| Flag | Default | Used with |
|---|---|---|
| `--nvidiagpuindex` | `0` | `hevc_nvenc`, `av1_nvenc` |
| `--vaapirendererpath` | `/dev/dri/renderD128` | `hevc_vaapi`, `av1_vaapi` |
| `--d3d12vagpuindex` | `0` | `hevc_d3d12va` |

> These flags select the GPU device for **encoding**. For hardware-accelerated **decoding** during `master` or `split`, use `--nvdec`, `--vaapidec`, `--d3d12dec`, or `--videotoolboxdec` instead. The corresponding GPU selection flags (`--nvidiagpuindex`, `--vaapirendererpath`, `--d3d12vagpuindex`) also apply when decoding.

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

Because a given VMAF target can require very different QP distributions depending on the source (e.g. clean animation vs. grainy film), mixing them into the same cache effectively poisons it. Use `--cacheprofile <name>` (e.g. `pixar_animation`, `grainy_90s`) to keep these histories separate. Without a profile, all runs with the same encoder and VMAF profile share the same cache.

| Flag | Short | Default | Description |
|---|---|---|---|
| `--statscachedir` | `-s` | OS cache dir (`~/.cache/sptenc` or equivalent) | Directory where QP statistics are stored |
| `--cacheprofile` | `-c` | *(none)* | Isolate cache history between content types |

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
ffmpeg [...] -c:v 'hevc_nvenc' -profile:v 'main10' -preset 'p7' -tune 'hq' -rc 'constqp' -qp 'X' -rc-lookahead 32 -spatial_aq 1 -temporal_aq 1 [...]
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
ffmpeg [...] -c:v 'av1_nvenc' -preset 'p4' -tune 'hq' -rc 'constqp' -qp 'X' -rc-lookahead 32 -spatial_aq 1 -temporal_aq 1 [...]
```

**av1_vaapi**
```bash
ffmpeg [...] -c:v 'av1_vaapi' -profile:v 'main' -rc_mode 'CQP' -global_quality 'X' [...]
```

## Output

The final output file is named:

```
<basename> [<encoder> SptEncoded].mkv
```

For example, encoding `Movie.mkv` with `libx265` produces `Movie [libx265 SptEncoded].mkv`.

The output is always Matroska (`.mkv`) because it is the most permissive container for stream copy.

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
- `ffmpeg` - compiled with `libx265` (or another supported encoder) and `libvmaf` support ([build guide](https://gist.github.com/hekmon/b273e55139183370c5000f766fccc128)) - can be used in WSL to get `libvmaf_cuda` support on Windows
- `ffprobe` - bundled with ffmpeg build
- `mkvpropedit` - from [MKVToolNix](https://mkvtoolnix.download/)

## License

MIT. See [LICENSE](LICENSE).
