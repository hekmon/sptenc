# sptenc - Split Encoder

`sptenc` (Split Encoder) is a scene-aware, [VMAF](https://github.com/Netflix/vmaf)-driven video transcoder.

It splits the input into scene-aligned segments, encodes each one independently, and validates the result against configurable VMAF thresholds before accepting it.
Failed segments are automatically re-encoded at a lower QP until all thresholds are met.
A final, complete VMAF comparison between the encoded output and original source is performed at the end, and its results are embedded into the output file's metadata tags.

This approach produces the smallest possible file without compromising the target quality defined by the VMAF profile.

> **Trade-off:** Achieving both smaller file size AND guaranteed quality comes at a cost: encoding time will be significantly longer than standard single-pass encoding, as multiple QP values are tested on each segment until all VMAF thresholds are met.

> **Inspiration:** sptenc is inspired by Netflix's [Dynamic Optimizer](https://netflixtechblog.com/dynamic-optimizer-a-perceptual-video-encoding-optimization-framework-e19f1e3a277f) framework, which pioneered scene-aware, perceptually-optimized video encoding.

## How It Works

1. **Scene detection** - The input is split at scene change boundaries (FFmpeg `scdet`), producing semantically coherent segments for consistent per-segment VMAF scoring. Alternatively, scene detection can be disabled to split at every I-frame instead.
2. **Per-segment encoding** - Each segment is encoded independently with `libx265` or `NVENC`.
3. **VMAF validation (post-encode)** - After encoding, each segment's VMAF scores are computed and checked against all configured thresholds. Any failure triggers a re-encode at a lower QP.
4. **Adaptive QP search** - sptenc maintains a QP statistics database per VMAF profile across runs (see below). This significantly accelerates convergence on subsequent runs.
5. **Best effort** - If QP=0 is reached and thresholds are still not met (e.g. pathological scene), the segment is accepted and flagged as "best effort" in logs. An alternative VMAF minimum can be set/used for such cases to avoid the large output of QP 0 encodes.
6. **Muxing & tagging** - Segments are merged into a single output file. A final VMAF comparison between the complete encoded file and original source is performed, with results displayed in logs and embedded in the output file's metadata tags.

## Key Features

- 🎯 **VMAF-driven encoding** - Guarantees a minimum perceptual quality level, not just a CRF or bitrate target
- 🎬 **Scene-aware segmentation** - Segments aligned with scene cuts for consistent quality (default mode); can be disabled to split at every I-frame instead
- 📊 **Multi-metric VMAF validation** - Combine mean, harmonic mean, median, percentiles (P1/P5/P10/P25), and worst-frame thresholds simultaneously; all must pass (AND logic)
- 🔍 **4 VMAF models, auto-selected** - Automatically uses 1080p or 4K model based on input resolution; add `-vmafneg` for NEG variants (recommended for upscaled/denoised/sharpened sources)
- 📋 **VMAF report embedded in output** - Final VMAF comparison results stored in the output file's metadata tags for full traceability
- 🧠 **Adaptive QP search with persistent stats** - Learns from previous encodes to dramatically reduce QP search iterations (see below)
- ⚡ **NVENC support** - GPU encoding for fast VMAF profile prototyping before the final `libx265` encode (which produces significantly smaller files at the cost of longer encoding times)
- 🌸 **Anime tuning** - `-anime` flag for `libx265` parameters optimized for animation
- 🖥️ **VMAF-CUDA** - Optional CUDA-accelerated VMAF computation (requires libvmaf with CUDA support) with the `-vmafcuda` flag. See the building guide below.

## Input Requirements

### Closed GOP Structure
**sptenc requires all input files to have a closed GOP (Group of Pictures) structure.** This means each GOP must be self-contained and not reference frames from previous or subsequent GOPs. This is essential because:
- When slicing an open GOP video, frames referencing other GOPs become undecodable and are dropped at decoding
- Accumulated dropped frames shorten the video duration, causing audio/video desynchronization
- VMAF comparison requires frame-exact alignment between source and encoded segments

If your source is not closed GOP, convert it first:

```bash
# TODO
```

> **Note:** Bluray remuxes are typically closed GOP, but often use a fixed length GOPs (e.g. 10s). This will lower the QP variation range and may result in lower efficiency. Even for Bluray remuxes with closed GOP, it's recommended to run the above command to use the encoder heuristics to recreate variable GOP length.

### Pre-segmented Input (Optional)

Instead of letting sptenc split the input automatically, you can provide an already-split directory of closed GOP segments:

```bash
./sptenc -input ./gop_dir/ -source original_with_audio.mkv
```
When using a pre-segmented directory, you can also use `-source` to specify the original file with audio tracks for final remuxing.

## Quick Start

### Basic encode - VMAF mean ≥ 93 (default)
```bash
./sptenc -input video.mkv
```

### Strict quality with multiple thresholds
```bash
./sptenc -input video.mkv -vmafmean 95 -vmafp5 85 -vmafmin 70
```

### Anime source
```bash
./sptenc -input anime.mkv -anime -vmafmean 95 -vmafp5 85
```

### Upscaled or denoised source - use VMAF NEG
```bash
./sptenc -input upscaled.mkv -vmafneg -vmafmean 93
```

### Fast VMAF profile prototyping with NVENC on the second GPU
```bash
./sptenc -input video.mkv -nvenc -nvdec -vmafcuda -gpu 1 -vmafmean 93
# Once happy with the profile, re-run without -nvenc for the final smaller encode
```

### Pre-segmented GOP directory
```bash
./sptenc -input ./gop_dir/ -source original_with_audio.mkv -vmafmean 95
```

## VMAF

### Models

| Resolution | Standard Model | NEG Model |
|---|---|---|
| < 4K | `vmaf_v0.6.1` | `vmaf_v0.6.1_neg` |
| ≥ 4K (2160p) | `vmaf_4k_v0.6.1` | `vmaf_4k_v0.6.1_neg` |

The model is **automatically selected** based on input resolution. Use `-vmafneg` when the source has been upscaled, sharpened, or denoised - standard models will over-score such content.

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
| Harmonic mean | `-vmafhmean` | **93** | Penalizes local dips; **default gate** because it cannot under-deliver: hmean ≥ T mathematically implies mean ≥ T |
| Arithmetic mean | `-vmafmean` | disabled | Average quality. Redundant as a gate whenever hmean or a low percentile is enabled — kept for reporting and for external comparability (Netflix 93 convention, all published ladders use arithmetic mean) |
| Median | `-vmafmedian` | disabled | ≥ 50% of frames at or above T |
| Percentile 25 | `-vmafp25` | disabled | ≥ 75% of frames at or above T |
| Percentile 10 | `-vmafp10` | disabled | ≥ 90% of frames at or above T |
| Percentile 5 | `-vmafp5` | disabled | ≥ 95% of frames at or above T |
| Percentile 1 | `-vmafp1` | disabled | ≥ 99% of frames at or above T |
| Min | `-vmafmin` | disabled | 100% of frames at or above T — strictest floor. Can over-fire on transient frames (title cards, flash frames) and inflate bitrate |
| Min fallback | `-vmafminalt` | disabled | Best-effort ceiling for segments that cannot satisfy `-vmafmin` even at QP 0 |

**Fixed relationship:** `min ≤ p1 ≤ p5 ≤ … ≤ hmean ≤ mean`

Consequences used everywhere below:
- **Gate composition:** one strict measure > several modest ones. Enabling `-vmafhmean T` makes `-vmafmean T` a tautology — don't gate both.
- **Diagnostic inversion:** the **mean − hmean gap** is the signal for "uniformly good" (small gap → QP is well tuned) vs "good on average with bad patches" (large gap → per-scene splitter cut, or QP granularity issue, or move the gate to hmean/min). The mean is always logged for this reason even when it's not gated.

**Rules:**
- All enabled thresholds must pass simultaneously (AND logic)
- Set any threshold to `-1` to disable it

> 💡 **Tip:** Start with `-vmafmean 93` alone with the `-debug` flag to inspect each encode attempt's VMAF score and identify problematic scenes, then move the actual gate to `-vmafhmean 93`, and add `-vmafp5` or `-vmafp1` only if a profile demands explicit percentile guarantees. Running mean + hmean gates at the same value is redundant — only one of them is real work.

### Recommended Values

| Use Case | Gate | Target value |
|---|---|---|
| "I am afraid of deleting my lossless master file" | `-vmafhmean` + `-vmafmin` | `99` + `93` |
| Archival / mastering | `-vmafhmean` | `95` |
| General streaming / VOD | `-vmafhmean` | `93` (default) |
| Live sports / fast motion | `-vmafhmean` | `93` |
| Mobile / bandwidth-constrained | `-vmafmean` | `85–90` |
| Quality consistency critical | `-vmafhmean` or `-vmafp5` | `90` |

**Why hmean everywhere except mobile:** hmean ≥ T implies mean ≥ T, so an hmean gate is strictly stronger than the classic mean 93 contract for no ambiguity cost — it simply also refuses segments with local dips. The mobile row keeps `-vmafmean 85–90` deliberately: on the cheap rung, average-level maximization *is* the contract, and an hmean gate would inflate bandwidth without any perceptible benefit at that distance/tier.

**Why the paranoid-master row is hmean + min and not mean + min:** `hmean ≥ 99` entails `mean ≥ 99`, so gating mean 99 was redundant. The `-vmafmin 93` floor survives alongside because harmonic mean punishes dips it *recognizes* but still tolerates genuinely isolated single frames; min closes that last gap.

> **93 vs 95?** The 93 target comes from a RealNetworks white paper showing it delivers content that is *"indistinguishable from original or with noticeable but not annoying distortion"* for most viewers ([StreamingLearningCenter — analysis](https://streaminglearningcenter.com/encoding/optimal_encoding_ladder_vmaf.html)). The 95 target, from a more recent paper, is the lowest score at which content is *"on average subjectively indistinguishable from the original"* — a higher bar that costs ~1400 kbps extra at the top rung. With `-vmafhmean 93` as default you get the 93 average backed by a no-bad-shots guarantee; the jump to 95 remains an explicit opt-in.

## Adaptive QP Search

One of sptenc's core performance features. After each complete encode job finishes (all segments processed), sptenc stores QP statistics **per VMAF profile** (i.e. the combination of enabled metrics and their target values), weighted by the number of GOPs/segments.

This results in significantly fewer encode iterations and improved encode time. Here are some examples:

| Encode job | Without stats | With stats |
|---|---|---|
| Small episode | ~7h30 | ~4h |
| Film | ~85h | ~60h |

The stats files are **profile-specific**: changing any VMAF threshold value will change the file name and start a fresh learning curve for this new profile.

### Persistent Stats from Previous Runs

QP statistics are persisted across runs and used to accelerate future encodes with the same VMAF profile:

- **Mean QP** - used as the starting point for the QP search on the next encode, avoiding blind starts from an arbitrary default
- **Standard deviation** - used as the QP search increment when exploring QP values outside the already-observed range

### Interpolation Within Already Observed Range

When the next QP to test falls **inside the already-observed range** (e.g., QP 19 and QP 23 have been computed and QP 19 is ok but QP 23 is not, the next candidate will be somewhere between them), sptenc uses **Fritsch-Butland monotone cubic interpolation** on N dimensions (one per active VMAF metric) to predict the next QP candidate, rather than probing blindly.

## NVENC vs libx265

| | libx265 (default) | NVENC (`-nvenc`) |
|---|---|---|
| Output file size | ✅ Optimal | ❌ ~2× larger |
| Speed | Slower | ✅ Much faster |
| Recommended for | Final archival encode | VMAF profile prototyping |

### Base ffmpeg encode options

Under the hood, here are the base ffmpeg encoding options used by `sptenc`. There can be others depending on options specified to `sptenc` (like `-nvdec`).

#### libx265

```bash
ffmpeg [...] -c:v 'libx265' -profile:v 'main10' -preset 'slow' -qp 'X' -x265-params 'aq-mode=3:hevc-aq=1' [...]
``` 

#### NVENC

```bash
ffmpeg [...] -c:v 'hevc_nvenc' -profile:v 'main10' -preset 'p7' -tune 'hq' -rc 'constqp' -qp 'X' -rc-lookahead 3 -spatial_aq 1 -temporal_aq 1 [...]
```

## Installation

```
TODO
```

**External Dependencies:**
- `ffmpeg` - compiled with `libx265` and `libvmaf` support ([build guide](https://gist.github.com/hekmon/b273e55139183370c5000f766fccc128))
- `ffprobe` - bundled with ffmpeg build
- `mkvpropedit` - from [MKVToolNix](https://mkvtoolnix.download/)

## License

MIT. See [LICENSE](LICENSE).
