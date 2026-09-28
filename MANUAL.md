# Split Encoder - Manual

Start with the [README](README.md). This is the "tell me everything" page. The measurements behind its figures on quality, banding, search time, file size and disk space are in [BENCHMARKS](BENCHMARKS.md).

1. [Input Requirements](#input-requirements)
2. [Disk space](#disk-space)
3. [VMAF](#vmaf)
4. [Encoders](#encoders)
5. [Scene Detection and Threshold Selection](#scene-detection-and-threshold-selection)
6. [Adaptive QP Search](#adaptive-qp-search)
7. [Output](#output)
8. [Base ffmpeg encode options](#base-ffmpeg-encode-options)
9. [Installation details](#installation-details)
10. [Compared to other approaches](#compared-to-other-approaches)

## Input Requirements

### Constant Frame Rate (CFR)
**Variable frame rate (VFR) content is not supported.** sptenc works on a constant frame grid: segments are cut, counted and put back together by frame index, and the merged video is timestamped at the frame rate of the source. A VFR source would come out retimed: a file mixing 24 and 30 fps parts came out with its 30 fps parts slowed down to 24 fps, its video no longer lasting what its audio does. VMAF does not see it (it pairs the frames of both videos by their index), so sptenc rejects VFR sources before encoding anything: at startup when the frame rates the file declares disagree, then from the measured duration of every frame once they are counted, before the master is written (a VFR Matroska file declares the same rates as a constant one). With a pre-split directory, only the frame rates the segments declare are checked.

### Progressive content
**Interlaced content is not supported.** Nothing in the pipeline handles fields: an interlaced source would be encoded as progressive frames with the combing baked in, in a file whose container still declares it interlaced while its frames say progressive. VMAF would validate it all the same (combed pictures compared with the same combed pictures). sptenc rejects inputs declared as interlaced at startup: deinterlace them first, the method being a choice on the content that is yours to make. This relies on what the stream declares: progressive pictures stored in an interlaced stream (PsF) are rejected too.

### Pre-segmented Input (Optional)

Instead of letting sptenc split the input automatically, you can provide an already-split directory of segments. Files must be `.mkv` or `.mp4` and are processed in **alphabetical order** — name them accordingly (e.g. `seg_01.mkv`, `seg_02.mkv`) to preserve scene order. All segments must share the same codec and frame rate.

```bash
./sptenc encode ./gop_dir/ output.mkv --original-file original_with_audio.mkv
```

When using a pre-segmented directory, `--original-file` (alias `-f`) is **required** so sptenc can remux audio, subtitles, and other streams into the final output.

## Disk space

sptenc cuts and measures a lossless copy of the video, the master (FFV1 on 10 bits, see [Base ffmpeg encode options](#base-ffmpeg-encode-options)), and that copy is large. Measured on two 1080p 8-bit Blu-ray remuxes:

| Source | Source file | Master | Master per hour |
|---|---|---|---|
| 26 min anime episode | 4.04 GiB | 14.29 GiB | 33.0 GiB |
| 101 min live-action film | 23.77 GiB | 59.30 GiB | 35.2 GiB |

Its size depends on the content, and a 4K source has four times as many pixels to store. What each command holds at its peak, besides its output:

| Command | At its peak | The film above |
|---|---|---|
| `encode` on a file | The segments: the master is written directly as its segments, never whole | 59 GiB, plus the encodes |
| `batchsearch` | The master, kept for every candidate, and the segments of the candidate being searched, deleted once it is done: about twice the master | 119 GiB, plus the encodes |
| `encode` on a pre-split directory | The segments merged back into one file, the reference of the final VMAF | 59 GiB, plus the encodes |
| `split` | The segments, about the master, in the output directory: a source's master is written directly as its segments, never whole | 59 GiB |
| `master` | The master, at its output path | 59 GiB |

The encodes are the encoded segments kept for the output and their merge: twice the encoded video, small next to the master (1.8 GiB each for the film with `hevc_nvenc` at the default). The output file holds the encoded video and every other stream of the source.

Everything but the outputs goes to the temporary directory, `--tmp-dir`: the system's by default (`$TMPDIR`, or `/tmp` when it is not set, on Linux and macOS; `%TMP%` or `%TEMP%` on Windows). Point it to a disk that has the room: where `/tmp` is a tmpfs, it lives in memory, limited to half of it by default, far from what a film needs (`df -h /tmp` tells). It is deleted at the end of a run, or when you interrupt one, but a failed run leaves it in place for inspection, and so does `--debug`: its path is printed, delete it yourself.

## VMAF

### Models

By default, sptenc scores with the [VMAF v1 models](https://github.com/Netflix/vmaf/blob/master/resource/doc/models_v1.md), released by Netflix in June 2026 and built into libvmaf 3.2.0 and newer. Compared with the v0.6.1 generation, they add a banding feature ([CAMBI](https://github.com/Netflix/vmaf/blob/master/resource/doc/cambi.md)), a chroma one and an additive impairment term, and drop VIF. The enhancement gain the v0 NEG models removed is clamped in every v1 model: "NEG is enabled by default for VMAF v1 without a need for a separate model" ([Netflix](https://netflixtechblog.com/vmaf-v1-good-is-not-good-enough-60d7e4244ea8)). sptenc gates on their score without its banding term, and measures the banding the encoder adds on its own: see [Fidelity and banding](#fidelity-and-banding).

| Model | Viewing condition | Scale | sptenc |
|---|---|---|---|
| `vmaf_v1.0.16_3d0h` | 1080p display, from 3 times its height | 0–100 | Selected below 2160 lines |
| `vmaf_v1.0.16_1d5h_2160` | 2160p display, from 1.5 times its height | 0–100 | Selected from 2160 lines |
| `vmaf_v1.0.16_5d0h` | Phone (1080p), from 5 times its height | 0–100 | Never selected, used with `--vmaf-model`: from that distance small artifacts go unseen, and they are what a quality floor is about |
| `vmaf_v1.0.16_3d0h_2160` | 2160p display, from 3 times its height | 0–110 | Never selected, used with `--vmaf-model`: same reason, and scores above 100 a threshold can not ask for |
| `_hfr` variants of the four | The same, for ~50/60 fps content | | Never selected, used with `--vmaf-model`: Netflix calls their frame rate handling "an area of active improvement" |

The model is **selected from the height of the input**: a 3840×1600 cropped film gets the 1080p model, a 3840×2160 one the 4K model. `--vmaf-model` forces another one, to judge a 1440p source as it would be seen on a 4K display for instance: a model made for another display than the source's is only a warning, contradicting the rule being the point of forcing it. It takes any model your libvmaf knows: the ones above, [the v0 ones](#the-v0-models), and models released after this version of sptenc, which it says it does not know (their viewing condition and scale are yours to check). The model is printed at the start of a run, written in the output tags (`sptenc_vmaf_model`) and part of the [QP cache](#persistent-stats-from-previous-runs) identity. Keep the viewing condition in mind when interpreting scores for other displays.

> **Requirements.** libvmaf 3.2.0 or newer. `encode`, `batchsearch` and `vmaf` load the model on two frames generated by ffmpeg before anything starts, and `sptenc check` lists the two models it selects with your libvmaf version. Once the source is known, and before its master is written, two frames of its own size are scored with the model of the run, and go through the banding measure when the [CAMBI gate](#fidelity-and-banding) is on. libvmaf can not measure pictures below a minimum size, a few hundred pixels depending on the model and on the shape of the picture, and it does not always say so: it crashes, or ffmpeg exits successfully without a complete report. sptenc then stops, naming the size and the measure that refused it (the limits measured are in [BENCHMARKS](BENCHMARKS.md#minimum-picture-sizes)).

#### The v0 models

sptenc v0.1.0 scored with the previous generation, the v0.6.1 models. libvmaf still builds them in and `--vmaf-model` still takes them, but sptenc never selects them: **they measure luma only, and do not see banding** (their NEG variants barely do). [What VMAF sees](#what-vmaf-sees-and-what-it-does-not) measures each of these defects with both generations. A run forcing one warns about it. With no banding feature, their score has nothing to take out: it is their fidelity score, `--vmaf-original` changes nothing, and the [CAMBI gate](#fidelity-and-banding) is off unless a CAMBI threshold asks for it. Without it, the run scores, prints, tags and caches as sptenc v0.1.0 did.

| Model | Viewing condition |
|---|---|
| `vmaf_v0.6.1` | 1080p display, from 3 times its height |
| `vmaf_v0.6.1neg` | The same, with No Enhancement Gain: enhancements such as sharpening can not raise the score ([NEG mode](https://github.com/Netflix/vmaf/blob/master/resource/doc/models_v0.md#disabling-enhancement-gain-neg-mode)) |
| `vmaf_4k_v0.6.1` | 2160p display, from 1.5 times its height |
| `vmaf_4k_v0.6.1neg` | The same, with No Enhancement Gain (built into libvmaf 3.0 and newer) |

Forcing one is for comparisons: scoring the way the [published anchors](#for-reference-the-v0-anchors) were measured, or the way sptenc v0.1.0 did (the QP statistics it gathered with that model are then reused, see [the cache](#persistent-stats-from-previous-runs)). v0.1.0 could also compute them on an NVIDIA GPU: see [why VMAF now runs on the CPU](#vmaf-runs-on-the-cpu).

### Fidelity and banding

The v1 models put two different measures into one score. Their full-reference features (ADM with an additive impairment term, motion, chroma) measure what the encode lost against its source. CAMBI, fed to the same model, rates how banded the encoded picture is on its own, without looking at the source: the banding a source already has counts as if the encoder had made it. A banded source scores below 100 against itself: down to 90.9 on the segments of the two Blu-ray remuxes measured (see [BENCHMARKS](BENCHMARKS.md#sources-against-themselves)). Behind a gate, that is a penalty for something the encoder did not do, and it even bends the search: an encoder smooths the source's steps, so CAMBI finds the encodes less banded as the QP rises, and a dark scene of the film scored higher at QP 4 than at QP 0. sptenc promises a floor on what the encoder did, so it takes the two measures apart:

- **The VMAF thresholds gate fidelity**: the score of the same model with its CAMBI term set to zero (libvmaf's `cambi_max_val` option), which scores a source 100 against itself. It never rose with the QP on the real segments measured, where the model's own score rose on most of them. On pictures CAMBI rates 0, coarse posterization included, both scores are the same.
- **The banding the encoder added is gated on its own**, by CAMBI in full-reference mode: it rates each frame of the encode and the same frame of the source, with the settings of the v1 models, and keeps the difference when the encode is more banded (none when the encoder removed some). Fidelity alone does not see it well: a smooth gradient turned into 8-bit-like steps passes fidelity at 95, while CAMBI rates the steps far more banded than the gradient (see [What VMAF sees](#what-vmaf-sees-and-what-it-does-not)).

**Reading the banding values.** CAMBI rates how banded a picture looks, from 0 (none) up: "a CAMBI score around 5 is where banding starts to become slightly annoying", and "the maximum CAMBI observed in a sequence is 24 (unwatchable)" ([CAMBI](https://github.com/Netflix/vmaf/blob/master/resource/doc/cambi.md)). The gate reads the banding the encoder added: for each frame, how much higher the encode rates than the same frame of the source, 0 when it does not rate higher, then its mean over the frames of the segment and its worst frame. The arithmetic mean, not the harmonic mean of the VMAF gate: a harmonic mean leans toward the lowest values, which for a score are the worst frames, but here the frames with nothing added (90 frames at 0 and 10 at 5 average 0.5, their harmonic mean is 0.09). A mean of 1 is an encode rated one point above its source on average, the frames not rated higher counting 0. It is not the difference between their averages: over the anime episode measured, at the default, the source rates 7.0 on average (it is banded already), the encode 1.8 (the encoder smoothed most of it), and the banding added is 0.004 on average and 3.0 on the worst frame. The final report and the [output tags](#metadata-tags) give all of them, and `--debug` prints them at every banding measure of a segment.

| Metric | Flag | Default | Meaning (threshold T) |
|---|---|---|---|
| Mean added banding | `--cambi-mean` | **1** | The banding the encoder added, averaged over the frames of the segment: at most T |
| Worst frame | `--cambi-max` | disabled | No frame of the segment with more than T of added banding |

Set a threshold to `-1` to disable it, both to turn the gate off; 0 is a threshold (no added banding at all). They apply to every command that encodes, `batchsearch` and its final encode included.

**How the search uses them.** The VMAF search runs first (see [Adaptive QP Search](#adaptive-qp-search)) and measures no banding. At the QP it found, the added banding is measured once: if the CAMBI thresholds pass, that QP is kept and nothing more is encoded. Otherwise sptenc tries the QP below, then the next one, each for both the VMAF and the CAMBI thresholds (a QP the VMAF search already encoded only needs its banding measured), and keeps the first one passing both: the highest QP at or below the VMAF search's. It never jumps: the added banding does not always shrink as the QP goes down (the worst frame of a segment of the episode adds more at QP 20 than at 28), a jump could skip the QP to keep, and every QP too low is size for nothing.

When no QP passes down to the encoder's lowest, the segment is a **CAMBI best effort**, counted apart from the VMAF ones: it keeps the highest QP passing the VMAF thresholds and the mean one, the worst frame threshold given up, and the QP the VMAF search found if even the mean could not be met. A best effort must not buy size for a threshold nothing meets. Knowing that nothing passes takes trying every QP down to the lowest. A segment that is a VMAF best effort has its banding measured all the same, and can be both.

**A mean of 1 is a safety net.** On the two contents measured, the gate lowered no segment: at the QPs the VMAF search picked, the highest mean was 0.80, on grain over a dark wall that `hevc_nvenc` flattened into smooth patches, and no other segment came above 0.25 (see [BENCHMARKS](BENCHMARKS.md#the-default-on-the-two-contents)). It is there for smooth gradients, where encoders add bands while fidelity passes: on synthetic ones, it cost almost nothing on clean gradients and made a grainy one over 100 times the size fidelity alone gave it (see [BENCHMARKS](BENCHMARKS.md#the-cambi-gate-on-smooth-gradients)). A lower default would chase CAMBI's own floor: NVENC adds more than 0.5 to a clean radial gradient at every QP from 0 to 40.

**The worst frame is for strict limits**, master encodes for instance. What it catches on real content is short bursts of a few frames, hard to see: a studio logo whose fine texture the encoder flattened into plateaus one code apart, grain on a near-black background turned into flat blocks. Removing that last one made a segment of the episode four times the size the VMAF search gave it (see [BENCHMARKS](BENCHMARKS.md#the-worst-frame-threshold)).

**What it costs:** 12 to 15% more search time on both contents (`hevc_nvenc -C 6`), the banding being measured once per segment, at the QP the VMAF search found (see [BENCHMARKS](BENCHMARKS.md#search-time)).

**`--vmaf-original`** gates on the model's own score instead, CAMBI included, as libvmaf computes it and Netflix publishes it. It is there to compare with published v1 figures, not to encode: the banding already in the source then counts against every encode, and the files grow for it, by 31 to 41% on the two contents at harmonic mean 93 (see [BENCHMARKS](BENCHMARKS.md#gates-on-the-two-contents)). The CAMBI gate still applies: it measures something else. Its QP statistics are [cached](#persistent-stats-from-previous-runs) apart from fidelity's, and it changes nothing with a model without banding feature, which the run says.

The `vmaf` command reports the same way: fidelity, or the original score with `--vmaf-original`, the harmonic mean of the other one, and the banding the distorted video adds to the reference.

### Quality Score Reference

VMAF scores are relative to the viewing conditions the models were trained on (1080p display at 3 times its height; 4K display at 1.5 times its height), so treat them as comparable only within the same viewing context.

No subjective study has been published on the v1 scale yet. Netflix built it to read like v0's: "we calibrated the VMAF v1 scale to align with v0 via a score transform, so that the new algorithm preserves the meaning of the numbers" ([Netflix](https://netflixtechblog.com/vmaf-v1-good-is-not-good-enough-60d7e4244ea8)). Fidelity is that scale without its banding term. The published anchors below, established with the v0 models, therefore remain the reference to pick a first value, and sptenc's defaults come from them. Where fidelity departs from them on real content is [measured below](#fidelity-against-v0-on-real-content).

#### For reference: the v0 anchors

These anchors were measured with the v0 models, which sptenc can still score with, blind spots included (see [the v0 models](#the-v0-models)).

| VMAF Score | Verifiable interpretation | Source |
|---|---|---|
| **95–100** | Subjectively indistinguishable from the original, on average (95 is the lowest such score) | Subjective study by Kah et al. ([Proc. SPIE 11842, 2021](https://doi.org/10.1117/12.2593952)), cited by Ozer |
| **~93** | "Either indistinguishable from original or with noticeable but not annoying distortion" for the vast majority of viewers (4K clips) | Rassool, RealNetworks ([IEEE BMSB 2017](https://doi.org/10.1109/BMSB.2017.7986143)), cited by Ozer |
| **40–70** | Typical range of an SD encode at 480p | A contact at Netflix quoted by Ozer (2017) |
| **~20** | A 240p encode at CRF 28, the lowest quality the model was trained on | A contact at Netflix quoted by Ozer (2017) |

See [StreamingLearningCenter — Optimal encoding ladder with VMAF](https://streaminglearningcenter.com/encoding/optimal_encoding_ladder_vmaf.html) and [Netflix via StreamingLearningCenter — Just Noticeable Difference](https://streaminglearningcenter.com/codecs/finding-the-just-noticeable-difference-with-netflix-vmaf.html).

Intermediate bands commonly cited elsewhere (e.g. "80–90 = good quality with minor artifacts", "70–80 = medium quality") are editorial interpolations between these anchors, not verbatim quotes from a verifiable source.

#### Fidelity against v0 on real content

On two 8-bit Blu-ray remuxes, a 26 min anime episode and a 101 min live-action film, fidelity was stricter than v0 on most segments, at 93 as at 95. At 93, with the encoder settings of sptenc v0.1.0 (NVENC's adaptive quantization on), the files came out 15% and 53% larger than those of v0.1.0 gated on v0 at 93, and v0 scores them 94.1 and 95.0. Not on every segment: on the episode, v0 scores its end credits (text on black, where v1 is more lenient than v0) below 93, and some segments just under it. With the adaptive quantization off, as this version runs NVENC, the files of the default are 1% and 8% larger than those of v0.1.0, and v0 scores them 94.3 and 95.1, with the end credits and the studio logo of the film below 93 as well. Every run is in [BENCHMARKS](BENCHMARKS.md#gates-on-the-two-contents).

The defaults and the values recommended below still come from the v0 anchors. sptenc promises a quality floor, and a stricter floor is the side to err on: if yours proves more demanding than you need, encode a few segments of your content at a lower value, look at them, and lower the gate.

### What VMAF sees, and what it does not

Every guarantee sptenc makes is a guarantee on a VMAF score and on CAMBI: it is worth what the metrics are worth. VMAF is a good predictor of perceived quality for what it was built for, compression and scaling artifacts on the picture structure. The v1 [models](#models) see defects the v0.6.1 ones were blind to, not all of them. Measured on synthetic 10-bit 1080p clips (`testsrc2` and a dark gradient from ffmpeg's `lavfi`, see [BENCHMARKS](BENCHMARKS.md#synthetic-clips)), scores given as v0.6.1 / v0.6.1 NEG / v1 fidelity:

- **Color, partly.** v0 only measures luma. v1 has a chroma feature: a copy with its hues rotated by 90° scores 66.0 with fidelity (99.7 / 99.6 with v0, the score of the untouched copy), one with heavily blurred chroma 66.2. But a fully desaturated (grayscale) copy still scores 100: the chroma feature reacts to changes within the chroma planes, not to the color being gone. In practice encoders quantize luma and chroma together, so a segment passing is not expected to be damaged on chroma alone: this is an expectation, not something sptenc verifies.
- **Banding, seen twice.** A smooth dark gradient reduced to 18, 10 and 5 luma levels scores 100 at every step with v0 (the added edges count as an enhancement), 96 with its NEG variant, and 91.2, 87.9 and 82.1 with fidelity: coarse steps are for its full-reference features. Fine staircases are for [the CAMBI gate](#fidelity-and-banding): the same gradient in 8-bit-like steps (4 codes every 48 pixels) scores 99.3 / 96.3 / 95.2, a pass at 95, while CAMBI rates it 22.5, against 6.7 for the smooth gradient: 15.8 added, where the gate stops at 1 by default. The mandatory 10-bit output is still there to avoid creating banding in the first place.
- **What happens between frames.** Frames are scored one by one, then the scores are pooled. A quality changing from a frame to the next (flicker, keyframe pulsing) is not judged as such, only the score of each frame is. The [VMAF FAQ](https://github.com/Netflix/vmaf/blob/master/resource/doc/faq.md) itself mentions psycho-visual evidence that viewers weigh the worst frames more heavily than an arithmetic mean does: this is what the percentile and minimum gates are for.
- **HDR.** Netflix has not released an HDR model, v1 included: "We also plan to release an HDR version enhanced by the v1 improvements" ([Netflix](https://netflixtechblog.com/vmaf-v1-good-is-not-good-enough-60d7e4244ea8)). sptenc computes VMAF on HDR sources as on any other, without any tone mapping, and CAMBI with the transfer function of standard dynamic range pictures, its default: take these scores as an indication, not as a validated measure.

If your content is exposed to one of these (flicker, HDR), have a look at a few segments yourself before trusting the numbers and deleting a source.

### sptenc thresholds

VMAF scores range from 0 to 100 with the models sptenc selects. A difference of about 6 points is often quoted as one Just Noticeable Difference (JND): a rule of thumb from a contact at Netflix ([quoted by Ozer](https://streaminglearningcenter.com/codecs/finding-the-just-noticeable-difference-with-netflix-vmaf.html)), established with the v0 models, not a threshold measured on your content.

| Metric | Flag | Default | Meaning (threshold T) |
|---|---|---|---|
| Harmonic mean | `--vmaf-hmean` | **93** | Average quality, never above the mean. **The default, and the gate to use alone** |
| Arithmetic mean | `--vmaf-mean` | disabled | Average quality, libvmaf's default pooled score. The gate to pair with tail gates |
| Median | `--vmaf-median` | disabled | ≥ 50% of frames at or above T |
| Percentile 25 | `--vmaf-p25` | disabled | ≥ 75% of frames at or above T |
| Percentile 10 | `--vmaf-p10` | disabled | ≥ 90% of frames at or above T |
| Percentile 5 | `--vmaf-p5` | disabled | ≥ 95% of frames at or above T |
| Percentile 1 | `--vmaf-p1` | disabled | ≥ 99% of frames at or above T |
| Min | `--vmaf-min` | disabled | 100% of frames at or above T — strictest floor. Can over-fire on transient frames (title cards, flash frames) and inflate bitrate |

**Mathematically guaranteed relationships:** `min ≤ p1 ≤ p5 ≤ p10 ≤ p25 ≤ median`; `min ≤ hmean ≤ mean`.

**What the harmonic mean does not do.** The harmonic mean libvmaf computes (`n / Σ 1/(score+1) − 1`) stays very close to the mean on the scores sptenc works with, and only falls well below it when some frames are very bad: 2 frames at 40 in a 120-frame segment otherwise at 95 take it to 92.9 (mean 94.1). But 3 frames at 70 in a 100-frame segment otherwise at 94.5 leave it at 93.5 (mean 93.8): that segment passes an hmean 93 gate. The harmonic mean is a small margin on the average, not a guard against bad frames: bounding those is the job of the percentile and min gates.

How to compose a profile:
- **A single gate: the harmonic mean** (the default). It is never weaker than the mean at the same value, and costs very little more.
- **Tail gates (percentiles, min): pair them with the mean**, and disable the harmonic mean (`--vmaf-hmean -1`). The tail gates bound the bad frames, the mean sets the overall level. Gating both means at the same value would be redundant anyway: hmean ≥ T implies mean ≥ T.
- **Diagnostics:** the gap between the mean and a low percentile or the min tells a uniformly good segment from one good on average with bad patches. The gap between the mean and the harmonic mean is too small to tell. The final VMAF of the file is always logged with every statistic, and `--debug` logs them for every attempt of every segment.

**Rules:**
- All enabled thresholds must pass simultaneously (AND logic)
- Set any threshold to `-1` to disable it — remember that the harmonic mean gate is enabled by default, so pass `--vmaf-hmean -1` to silence it when you test other gates in isolation
- The [CAMBI thresholds](#fidelity-and-banding) are checked after them, at the QP they lead to

> 💡 **Tip:** run a first encode with `--debug` to see the statistics of every attempt, segment by segment: the segments whose p5 or min fall far below their mean are the ones a tail gate would act on. On a segment of fewer than 150 frames, p1 is its worst frame, the same as min (at the 5 s [minimum segment length](#the---min-segment-length-guardrail), 24 and 25 fps segments have 120 and 125 frames).

### Recommended Values

These values come from the [v0 anchors](#for-reference-the-v0-anchors): on both contents measured, fidelity at 93 and 95 was the stricter of the two on most segments (see [Fidelity against v0](#fidelity-against-v0-on-real-content)).

| Use Case | Gate | Target value |
|---|---|---|
| "I am afraid of deleting my lossless master file" | `--vmaf-mean` + `--vmaf-min` + `--cambi-mean` | `99` + `94` + `0.25` ¹ ³ ⁴ |
| Archival / mastering | `--vmaf-hmean` | `95` |
| General viewing, streaming, VOD | `--vmaf-hmean` | `93` (default) ² |
| Mobile / bandwidth-constrained | `--vmaf-hmean` | `85–90` |
| Quality consistency critical | `--vmaf-mean` + `--vmaf-p5` | `93` + `90` ¹ |

¹ With `--vmaf-hmean -1`: the tail gate bounds the bad frames, the mean sets the level (see above).

² fast-motion content needs no dedicated profile: motion-heavy segments fail the gate and converge to a lower QP automatically.

³ A minimum one JND below the source, which fidelity scores 100: about 6 points, the rule of thumb of [sptenc thresholds](#sptenc-thresholds), given for the rungs of a bitrate ladder (whole encodes) and applied here to single frames. At a mean of 99 it rarely acts: on 20 segments of the two contents measured, the worst frame stayed at 95 or above on 19, and 94 cost 1% more than 93 (see [BENCHMARKS](BENCHMARKS.md#gates-on-the-two-contents)).

⁴ A quarter of the default [CAMBI mean](#fidelity-and-banding). At the QPs of this profile, real content adds almost no banding: 0.009 at most on average over the frames of a segment, on the 20 segments measured, and nothing on the one adding 0.80 at the default. It acts on smooth gradients, where the default would let `hevc_nvenc` add 0.39 to a dark ramp (see [BENCHMARKS](BENCHMARKS.md#gates-on-the-two-contents)).

> **93 vs 95?** The 93 target comes from Rassool (RealNetworks, [IEEE BMSB 2017](https://doi.org/10.1109/BMSB.2017.7986143), [PDF](https://realnetworks.com/sites/default/files/vmaf_reproducibility_ieee.pdf)), who found that encoding to about 93 would serve the vast majority of viewers with content *"either indistinguishable from original or with noticeable but not annoying distortion"* (the two best ratings of the 5-level scale of his test, on 4K clips). The 95 target comes from Kah et al. ([Proc. SPIE 11842, Applications of Digital Image Processing XLIV, 2021](https://doi.org/10.1117/12.2593952)): VMAF 95 is the lowest score *"at which a video signal is on average subjectively indistinguishable from the original video signal"* (ITU-R BT.500 subjective tests on a 4K OLED TV viewed from twice its height), a deliberately higher bar. In Ozer's test on the *Meridian* clip, choosing 95 over 93 cost about 1400 kbps on the top rung (clip-specific, not universal). The default targets the first; the jump to 95 remains an explicit opt-in. Both studies used the v0 models: on the episode and the film [measured above](#fidelity-against-v0-on-real-content), v0 scores the fidelity encodes above the gate, at 93 as at 95.

### VMAF runs on the CPU

sptenc v0.1.0 could compute VMAF on an NVIDIA GPU (`--vmaf-cuda`, with `libvmaf_cuda`). The v1 models can not run there: libvmaf's CUDA code only covers the features of the v0 models, and loading a v1 model in `libvmaf_cuda` fails on its banding feature (`could not initialize feature extractor "Cambi_feature_cambi_score"`). VMAF is computed on the CPU, whatever the encoder; the GPU can still decode its inputs (see [Hardware decoding](#hardware-decoding)).

That is the price of seeing banding and color: with `hevc_nvenc`, the search of the episode measured took about 1.6 times as long as with v0 on the GPU. v1 itself costs less CPU than v0, but the search spends more of it decoding the lossless intermediate, twice per attempt, than computing VMAF, and the CAMBI gate decodes the segment and its encode once more: 12 to 15% of the search time (see [BENCHMARKS](BENCHMARKS.md#search-time)).

In exchange, CPU scores are reproducible: the same encode compared with the same reference got the same scores in every run, whatever the number of threads and of computations running at once, and `-C 1` and `-C 2` produced the same file. `libvmaf_cuda`'s scores varied from one run to the next.

## Encoders

sptenc supports multiple HEVC and AV1 encoders. The `--encoder` flag (alias `-e`) selects which one to use.

| Encoder | Codec | Type | Platforms |
|---|---|---|---|
| `libx265` | HEVC | CPU | All |
| `hevc_nvenc` | HEVC | NVIDIA GPU | All (10-bit HEVC encoding: GTX 10 series / Pascal or newer) |
| `hevc_vaapi` | HEVC | VAAPI GPU | Linux |
| `hevc_d3d12va` | HEVC | D3D12VA GPU | Windows |
| `hevc_videotoolbox` | HEVC | VideoToolbox GPU | macOS (Apple Silicon) |
| `libsvtav1` | AV1 | CPU | All |
| `av1_nvenc` | AV1 | NVIDIA GPU | All (Ada Lovelace / RTX 40 series or newer) |
| `av1_vaapi` | AV1 | VAAPI GPU | Linux (Intel Arc or Core Ultra, AMD RDNA 3 or newer) |

> **Note:** `libaom-av1` is not supported. It is too slow for sptenc's iterative per-segment QP search, where each segment may be encoded multiple times. `libsvtav1` is the CPU AV1 encoder sptenc supports. Run `sptenc check` to see which encoders your ffmpeg build supports.

> **Untested encoders:** `av1_vaapi` and `hevc_d3d12va` are implemented but have not been validated end to end (`hevc_vaapi` was, on Linux with an Intel iGPU, and the NVENC encoders on an RTX 5090 only). `av1_vaapi` needs a GPU that encodes AV1, and the author's only decodes it, as do the Intel iGPUs from Tiger Lake to Raptor Lake (11th to 14th generation Core, N100 included): ffmpeg then refuses to open the encoder, `No usable encoding entrypoint found for profile VAProfileAV1Profile0`. A GPU that can encode AV1 lists `VAEntrypointEncSlice` or `VAEntrypointEncSliceLP` for `VAProfileAV1Profile0` in `vainfo --display drm --device /dev/dri/renderD128`. `hevc_d3d12va` targets Intel and AMD GPUs on Windows, which the author does not have: NVIDIA users should use `hevc_nvenc` and `av1_nvenc`, not D3D12VA. Feedback from Intel or AMD hardware is welcome.

### Encoder selection vs file size

| | CPU encoders (`libx265`, `libsvtav1`) | Hardware encoders (NVENC, VAAPI, D3D12VA, VideoToolbox) |
|---|---|---|
| Output file size | Smaller | Larger |
| Speed | Slower | Several times faster |
| Recommended for | Final archival encode | VMAF profile prototyping, split threshold value search, and final encodes when time matters more than size |

Measured on one machine, an RTX 5090 with a 16 cores / 32 threads CPU, searching a 26 min 1080p episode at the default (see [BENCHMARKS](BENCHMARKS.md#encoders)): `hevc_nvenc` at preset p7, 6 segments at a time (`-C 6`), took 19 minutes; `libx265` at preset slow, 3 segments at a time (`-C 3`) and decoding on NVDEC, took 62 minutes, for a video stream 3% smaller than `hevc_nvenc`'s. The times compare these two setups, concurrency included, not the encoders alone (see below).

One episode is one data point: the size gap depends on the content, on the encoder settings (15% with NVENC's adaptive quantization on, which sptenc turns off) and on the score gated (31% gated on the model's original score, AQ on), and the times on the machine and on `-C`. On this one, the GPU search was limited by the CPU, which decodes the lossless FFV1 intermediate twice per attempt and computes VMAF, not by the GPU: a smaller CPU would have made it slower, smaller GPUs were not measured.

> **Concurrent segments (`-C`)**: segments are searched one at a time by default. The output is the same whatever the value, only the time it takes changes.
>
> **With a GPU encoder**, the number of encoding engines on the card is not the limit: a worker only feeds the encoder while it encodes and waits for VMAF the rest of the time, and the frames come from the CPU decoding the FFV1 intermediate, as measured above. On the RTX 5090 (three NVENC engines), `-C 6` kept the encoders busy 24% of the time and the CPU 85%: raise it until the CPU is saturated, the driver's encode session limit being the hard stop (an encode then fails to open its session).
>
> **With a CPU encoder**, a single encode already uses every thread of the machine, but does not keep a many-core CPU fully busy: on a 16 cores / 32 threads CPU, `libx265` at 1080p encoded 26% more frames per second with 2 concurrent segments (measured with VMAF v0 on the CPU). Expect less with fewer cores or bigger pictures (encodes alone, 2 at a time: +45% at 1080p, +21% at 2160p), mind the memory with 4K content, and measure on your machine.
>
> Segments started together can not learn from each other, so a few more attempts are needed at the beginning of a run: a cost only visible on short inputs. For the smallest file at the lowest search cost, `batchsearch --final-encode` searches the scene threshold on GPU then encodes the final file with the CPU equivalent: `-C` then only applies to the search, the final encode has its own `--final-concurrent-segments` as they do not run on the same hardware.

### GPU selection flags

When using a GPU encoder, you can target a specific device:

| Flag | Default | Used with |
|---|---|---|
| `--nvidia-gpu-index` | `0` | `hevc_nvenc`, `av1_nvenc` |
| `--vaapi-renderer-path` | `/dev/dri/renderD128` | `hevc_vaapi`, `av1_vaapi` |
| `--d3d12va-gpu-index` | `0` | `hevc_d3d12va` |

> These flags select the GPU device for **encoding**, and for **decoding** as well (see below).

### Hardware decoding

Decoding is hardware accelerated whenever sptenc knows it can be: a hardware encoder decodes with its own GPU. With a CPU encoder, or with `master`, `split`, `thresholds` and `vmaf`, ask for it with `--nvdec`, `--vaapi-dec`, `--d3d12va-dec` or `--videotoolbox-dec`; the device flags above apply to them. A decode flag contradicting the hardware encoder is refused. The same decoder serves the inputs of VMAF, which itself runs on the CPU. The lossless FFV1 intermediate has no hardware decoder: the CPU always decodes it.

- **Same pixels from H.264, HEVC, VP8, VP9, AV1 and VC-1 sources**, so the same output file: NVDEC decoded each of them to the very frames of ffmpeg's software decode (HEVC, VP9 and AV1 in 8 and 10 bits), and the specifications of all but VC-1 define the decoded pixels exactly. AV1's film grain is the one exception its specification allows: a decoder may synthesize a grain that only looks the same (NVDEC's matched the software decode's).
- **MPEG-1, MPEG-2, MPEG-4 Part 2 and MJPEG sources are always decoded by the CPU** (DVDs, older captures), whatever the encoder and the flags. Their decoded pixels depend on the inverse DCT of the decoder, whose error the MPEG-1, MPEG-2 and JPEG standards only bound: NVDEC's frames differed from ffmpeg's on every frame of a test clip of each (PSNR above 60 dB), and ffmpeg's own inverse DCTs do not agree either. Invisible, but the master, and the output with it, would not be the one a software decode gives.
- **A codec the decoder does not support** falls back to software with a warning, as do the four above. A profile it does not support within a supported codec (H.264 4:4:4 on NVDEC, for instance) is not detected: ffmpeg falls back to software by itself, except with a hardware encoder given a pre-split directory, where segments it can not decode make the encode fail (seen with NVENC).

## Scene Detection and Threshold Selection

Scene detection splits a video into independent segments, and each segment gets its own QP. The threshold controls how many boundaries are kept, which directly affects both quality visibility and file size. There is no single right threshold — the choice depends on which tradeoff you are willing to accept.

### Too fine: many short segments

A low threshold keeps almost every detected boundary. This gives hard passages their own QP and keeps VMAF metrics honest: in a short segment, bad frames have fewer good ones to hide among. A min or low percentile gate catches them; an average gate, harmonic mean included, only when they are many or very bad (see [sptenc thresholds](#sptenc-thresholds)).

The cost is keyframe bloat and B/P-frame starvation. Every boundary forces an I-frame, and runs shorter than a few seconds never let inter-frame referencing amortize the intra cost. File size inflates, and on very short segments the VMAF percentiles collapse onto the worst frames (below 150 frames, p1 is the worst frame; below 30, p5 is).

### Too coarse: few long segments

A high threshold discards weak boundaries, merging scenes into long runs. B/P-frame compression thrives, and file size drops — but the whole segment must bow to its hardest passage. Easy sections pay for quality they do not need.

More dangerously, a short complex passage inside a long easy segment can fail VMAF locally while the segment-wide average still passes. An average gate does not see it, and a percentile gate lets through any passage shorter than its share of the segment (p5 tolerates its worst 5%: 4.5 s of a 90 s segment). The bad frames are statistically invisible, undermining the guarantee that every part of the video meets your quality floor.

### The role of `batchsearch`

`batchsearch` automates the tedious work of testing multiple thresholds and picking the one that produces the smallest file while still passing your VMAF targets. Its objective is file size — it has no opinion on whether the winning threshold's segment lengths are short enough for their percentile metrics to be trustworthy.

If you care more about tight quality control than file size, skip `batchsearch`. Use the `thresholds` command to inspect distributions, pick a threshold manually, and run `encode`.

### The `--min-segment-length` guardrail

The `--min-segment-length` flag (alias `-L`, default 5s) removes boundaries that would create segments shorter than the given duration. This is a quality-floor guardrail: it prevents unreliable percentile metrics and B/P-frame starvation by merging short segments into their shorter neighbor.

It does **not** protect against the opposite problem. Segments longer than ~5 seconds may still be too long for your tolerance of the drowning risk. That judgment remains yours.

Use the `thresholds` command to preview the segment distributions a threshold would produce before committing to an `encode` or a `batchsearch` run. It is fast and produces no files.

### Reusing a threshold

`encode`, `split`, `thresholds`, and `batchsearch` all build scenes the same way, on the source file: the threshold picks the boundaries first, then `--min-segment-length` merges the short segments that remain. The scenes a threshold produces therefore depend only on that threshold and on `--min-segment-length` — not on the `--min-threshold`/`--max-threshold` range a search was run with.

`split --master` detects the scenes on the master it is given. They are the source's when the master holds the source's luma without loss, the only plane scene detection reads: a limited range source of 8 or 10 bits. A full range source's luma is converted to the limited range (see [Base ffmpeg encode options](#base-ffmpeg-encode-options)): on a test clip, its scene changes scored 14% lower on the master than on the source, and a threshold of 17 lost one of the four cuts `encode` kept. Split the source itself to cut where the other commands do.

This makes thresholds portable: a row of the `thresholds` table, or the best candidate reported by `batchsearch`, gives exactly the same scenes when passed to `encode -T` with the same `--min-segment-length`. A typical use is to run `batchsearch` on one episode and `encode -T <best>` on the rest of the season.

Reported thresholds look like `24.2765` rather than `24.277`: ffmpeg prints scene scores rounded to 3 decimals but compares thresholds against the unrounded score, so sptenc reports half a step below the printed score to guarantee the boundary is kept. Use the value as printed.

## Adaptive QP Search

sptenc's per-segment QP search converges on the highest valid QP (smallest file) in three steps, efficiently even on the first run, then checks the banding the encoder added:

1. **Smart start** — The first candidate is the mean QP of the segments already done in this run, seeded with the mean of previous runs (see [below](#persistent-stats-from-previous-runs)): the first segment starts from that seed, whose weight then fades as segments complete. Without any previous run, the seed is the midpoint of the encoder's QP range (e.g. QP 26 for libx265's 0–51 range).
2. **Bracketing** — Steps away from the starting point, one standard deviation further at each encode, to find one valid QP (passes VMAF) and one invalid QP (fails VMAF), closing the search range around the boundary. The standard deviation comes from the same statistics as the mean; without any previous run, it starts at a quarter of the QP range (e.g. 13 for libx265's 0–51 range).
3. **Interpolation** — Once bracketed, **Fritsch-Butland monotone cubic interpolation** forecasts the VMAF of the untested QPs within the range and picks the next one to encode, walking toward the highest valid QP without blind probing. A forecast only chooses the next encode: every QP kept was encoded and measured, and the next higher one encoded and found failing.
4. **Banding check** — At the QP found, the banding the encoder added is measured once. When a [CAMBI threshold](#fidelity-and-banding) fails, the QPs below are tried one at a time until one passes both.

This typically takes 3 to 5 encodes per segment, under 4 on average at the default on the contents measured (see [BENCHMARKS](BENCHMARKS.md#gates-on-the-two-contents)), where a plain bisection of libx265's 0–51 range takes about 6 whatever the content.

### Persistent stats from previous runs

After each encode job finishes, sptenc stores QP statistics **per encoder, VMAF model and VMAF profile** (i.e. the combination of the encoder, the 1080p or 4K model, the enabled metrics, and their target values), weighted by the number of segments. They are the QPs the VMAF search found, before the [CAMBI gate](#fidelity-and-banding) could lower them: the CAMBI thresholds are not part of the profile. Gating the model's original score (`--vmaf-original`) keeps statistics of its own. A `batchsearch` run only stores its winning candidate (plus the final CPU encode with `--final-encode`, on that encoder's own cache): the other candidates are the same content encoded again and would over-represent that file. Within the run though, every candidate starts from the QPs the previous candidates found: the same content is the best prior there is, so only the first candidate pays the learning cost.

These statistics only seed a search. In the run's own statistics they weigh a single segment, however many files they aggregate, so the file being encoded takes over within a handful of segments: a cache built on other content sets the starting point, it does not hold the search to its mean.

What is at stake is the number of encodes per segment. From a cold cache, the runs measured at the default already averaged under 4, the run's own statistics doing the learning from the first segments on. A segment can not take fewer than two, the QP kept and the next one failing, so what a warm cache can save is bounded by that: it has not been measured yet.

The cache provides:
- **Mean QP** — a better-informed starting point than the encoder midpoint
- **Standard deviation** — a tuned step size for bracketing, rather than a heuristic fraction of the range

The stats files are **profile-specific**: changing the encoder, the VMAF model (`--vmaf-model`, or a 4K source), any VMAF threshold value or the score gated (`--vmaf-original`) starts a fresh learning curve. The statistics of sptenc v0.1.0 were gathered with the v0 models, whose names are in their file names: later versions only read them again for a run forcing the same v0 model with `--vmaf-model`. `sptenc cache` lists the files with their model and the score they were gathered on, and deletes them by index.

### Cache isolation with profiles

Because a given VMAF target can require very different QP distributions depending on the source (e.g. clean animation vs. grainy film), mixing them into the same cache effectively poisons it. Use `--cache-profile <name>` (e.g. `pixar_animation`, `grainy_90s`) to keep these histories separate. Without a profile, all runs with the same encoder, VMAF model and VMAF profile share the same cache.

| Flag | Short | Default | Description |
|---|---|---|---|
| `--stats-cache-dir` | `-s` | OS cache dir (`~/.cache/sptenc` or equivalent) | Directory where QP statistics are stored |
| `--cache-profile` | `-c` | *(none)* | Isolate cache history between content types |

## Output

The output is always Matroska (`.mkv`) because it is the most permissive container for stream copy.

Every stream of the source other than video (audio, subtitles, attachments, data) is copied into the output. When every audio track is 16 or 24-bit little-endian PCM (`pcm_s16le`, `pcm_s24le`, as in MKV remuxes of Blu-ray discs), they are losslessly compressed to FLAC; otherwise audio is copied as is.

At the end, the whole output is scored against its source: every statistic of the gated score (fidelity, or the original score with `--vmaf-original`) and the harmonic mean of the other one, with a model that has a banding feature, and the banding when the [CAMBI gate](#fidelity-and-banding) is on: added by the encoder (mean and worst frame), in the source and in the encode (means). They are printed and written in the [tags](#metadata-tags).

Color metadata (`colorspace`, `color_trc` and `color_primaries`) is probed from the source and re-injected into the output container. The color range (`color_range`) is the exception: the output declares the range of the encoded video, and a full range ("PC") source comes out in limited range ("TV"), converted when the master is written (see [Base ffmpeg encode options](#base-ffmpeg-encode-options)). Tested with libx265, hevc_nvenc, libsvtav1 and av1_nvenc, on 8-bit and 10-bit full range sources. HDR metadata handling is still being validated.

You specify the output path explicitly as the final positional argument for file-producing commands (`encode`, `batchsearch`, `remux`, `master`, `concat`). Directory-producing commands (`split`) take an output directory in the same way.

### Metadata tags

The output file contains the following metadata tags on the video stream:

- `sptenc_url` and `sptenc_version` — tool provenance
- `sptenc_encoder` and `sptenc_encoder_preset` — encoder used
- `sptenc_segments_count` — number of segments
- `sptenc_stats_min_qp`, `sptenc_stats_max_qp`, `sptenc_stats_weighted_qp` — QP statistics
- `sptenc_vmaf_model` — VMAF model used
- `sptenc_vmaf_score` — the score the VMAF thresholds gated: `fidelity`, or `original` with `--vmaf-original` (omitted with a model without banding feature, whose score is both)
- `sptenc_vmaf_conf_*` — all enabled VMAF threshold values
- `sptenc_vmaf_result_*` — final scores of the gated score (min, p1, p5, p10, p25, median, hmean, mean, max)
- `sptenc_vmaf_original_result_hmean` — final harmonic mean of the original score when fidelity was gated (`sptenc_vmaf_fidelity_result_hmean` the other way around)
- `sptenc_best_effort_segments` — number of segments that stopped at minimum QP without reaching the target VMAF profile (omitted if zero)
- `sptenc_cambi_conf_mean`, `sptenc_cambi_conf_max` — the enabled CAMBI thresholds
- `sptenc_cambi_result_added_mean`, `sptenc_cambi_result_added_max` — the banding the encoder added over the whole file (mean, worst frame)
- `sptenc_cambi_result_source_mean`, `sptenc_cambi_result_encode_mean` — the banding of the source and of the encode (means)
- `sptenc_cambi_lowered_segments` — number of segments whose QP the CAMBI gate lowered (omitted if zero)
- `sptenc_cambi_best_effort_segments` — number of segments where no QP met the CAMBI thresholds (omitted if zero)

The CAMBI tags are written when the gate is on.

## Base ffmpeg encode options

These are the options sptenc passes to ffmpeg. They are fixed, not defaults: the QP is the only dial, so that the search, its statistics and the [QP cache](#persistent-stats-from-previous-runs) work the same way on every run, and what you tune is the **VMAF thresholds** and the **scene detection**, not encoder minutiae. If you need full control over every ffmpeg flag, ffmpeg itself is the right tool.

They aim at **a VMAF target met by every segment, at the smallest file size**, with one compromise: the search encodes every segment several times, so `libx265` runs at preset `slow` and `libsvtav1` at preset 3, not at their slowest settings. NVENC runs at its best preset, `p7`, the VA-API, D3D12VA and VideoToolbox encoders at ffmpeg's default settings, but for VideoToolbox's keyframe interval and B-frames (see [its options](#hevc)). NVENC's lookahead makes smaller files at the same QP. Its adaptive quantization is off: under constant QP it gives flat and static areas extra bits by NVIDIA's model of what the eye sees, which the VMAF gate does not reward. Turning it off made the outputs of the two contents measured 12% and 30% smaller at the default, with more banding added, still under the CAMBI gate, which measures it on every segment and lowers the QP only where it exceeds its threshold (see [BENCHMARKS](BENCHMARKS.md#nvenc-adaptive-quantization)).

> **10-bit output is mandatory.** Every encoder gets 10-bit 4:2:0 frames (`main10` for HEVC, `main` for AV1 which includes 10-bit): `yuv420p10le` for the CPU encoders, and `p010` for the hardware ones, the layout their APIs require (the same samples, with the chroma planes interleaved and the values stored in the high bits). Repacking one into the other is lossless: checked frame by frame for the CUDA and software conversions sptenc uses (VA-API converts on the GPU, not checked). 10-bit greatly reduces banding and improves compression efficiency at low bitrates — it is the modern baseline for quality encoding.
>
> The lossless FFV1 master is already stored as `yuv420p10le`. From a limited range ("TV") 8-bit or 10-bit 4:2:0 source, by far the most common kind, it holds every sample of the source without loss (8-bit values are shifted to 10 bits, exactly). Other sources are converted at that step, as the final encode would require anyway: 4:2:2 and 4:4:4 ones get their chroma subsampled (luma stays exact, and VMAF compares the encodes with this master: the chroma resolution lost here is not something it can see), sources deeper than 10 bits are reduced to 10 bits, and full range ("PC") sources are converted to limited range: without loss from 8 bits (each 8-bit level gets a 10-bit level of its own), with a slight loss from 10 bits (the 1024 luma levels of the source share the 877 of the limited range).

Under the hood, here are the base options used by sptenc. `X` is the QP value being tested for the current segment.

### HEVC

**libx265**
```bash
ffmpeg [...] -c:v 'libx265' -profile:v 'main10' -preset 'slow' -qp 'X' [...]
```

**hevc_nvenc**
```bash
ffmpeg [...] -c:v 'hevc_nvenc' -profile:v 'main10' -preset 'p7' -tune 'hq' -rc 'constqp' -qp 'X' -rc-lookahead 32 -spatial-aq 0 -temporal-aq 0 [...]
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
ffmpeg [...] -c:v 'hevc_videotoolbox' -profile:v 'main10' -q:v '101-X' -g 250 -bf 1 -bsf:v 'dump_extra' [...]
```

sptenc drives VideoToolbox's quality setting, from 1 (worst) to 100 (best), inverted so that the search sees a QP-like scale (X from 1 to 100, higher meaning smaller and worse). For HEVC, VideoToolbox turns each value into a fixed HEVC QP, the quantizer written in the stream, the same on every frame (Apple's documentation: "for some formats, this property will direct encoder to use a fixed quantization parameter"). Its steps are uneven: neighbouring values of sptenc's scale can make the very same encode, and some HEVC QPs are never reached, so the search tends to end further above its gate than with the other encoders (see [BENCHMARKS](BENCHMARKS.md#a-constant-qp-in-uneven-steps)). `-g 250` allows up to 250 frames between keyframes, `libx265`'s default maximum: without it, ffmpeg gives this encoder a keyframe every 12 frames, a generic default its VideoToolbox wrapper passes on. `-bf 1` allows B-frames, which VideoToolbox uses by default and ffmpeg turns off with its generic default of 0 (the wrapper only reads whether the value is 0, not a number of B-frames). Each made the episode 17% smaller than without it (see [BENCHMARKS](BENCHMARKS.md#keyframe-interval-and-b-frames)). `dump_extra` repeats the parameter sets before every keyframe: this encoder writes the quality into them, and without it the segments put back together would be decoded with the parameters of the first one.

### AV1

**libsvtav1**
```bash
ffmpeg [...] -c:v 'libsvtav1' -preset '3' -qp 'X' [...]
```

**av1_nvenc**
```bash
ffmpeg [...] -c:v 'av1_nvenc' -preset 'p7' -tune 'hq' -rc 'constqp' -qp 'X' -rc-lookahead 32 -spatial-aq 0 -temporal-aq 0 [...]
```

**av1_vaapi**
```bash
ffmpeg [...] -c:v 'av1_vaapi' -profile:v 'main' -rc_mode 'CQP' -global_quality 'X' [...]
```

## Installation details

Where the binaries are looked for:

| Binary | Linux / macOS | Windows | Override (global flag) |
|---|---|---|---|
| `ffmpeg` | `PATH` | `.\ffmpeg.exe` (current directory, not `PATH`) | `--ffmpeg-path` |
| `ffprobe` | `PATH` | `.\ffprobe.exe` (current directory, not `PATH`) | `--ffprobe-path` |
| `mkvpropedit` | `PATH` | `C:\Program Files\MKVToolNix\mkvpropedit.exe` | `--mkvpropedit-path` |

Example: `sptenc --ffmpeg-path /opt/ffmpeg/bin/ffmpeg encode [...]`. Run `sptenc check` to verify everything is found and usable.

## Compared to other approaches

### How does it compare to Av1an?

[Av1an](https://github.com/rust-av/Av1an) is the reference tool for scene based chunked encoding, and its Target Quality mode looks like what sptenc does: find, for each scene, the encoder setting reaching a metric score. If you know Av1an, this is the question you have. Both tools overlap, they do not aim at the same thing. As of Av1an's sources in September 2026 (commit `6487fa5`; its documentation site still describes some older options):

| | Av1an (Target Quality) | sptenc |
|---|---|---|
| Purpose | Encode faster by running several encoder processes in parallel; Target Quality is one of its modes | Enforce a quality floor on every scene; time (CPU encoder) or size (hardware encoder) is what is traded for it |
| What is promised for a scene | A score to aim at: a window around the target (±1% of a single value, or a range you give), searched with a limited number of probes (4 by default). The highest quantizer landing in the window is kept, or the probe closest to the target when none does | A floor: no limit on attempts, a segment is only accepted once it passes, or is flagged as best effort when even the lowest QP of the encoder can not pass |
| What is measured | Probes, scored on every frame and at 1920×1080 by default (`--probing-rate`, `--probe-res`, `--vmaf-res`). By default they are encoded with Av1an's own fast settings, and the chunk is then encoded again with yours, without being measured. With `--probe-video-params copy` (and no frame sampling, filter nor proxy), the probe kept is the chunk itself | The segments ending up in the output file, always, at their own resolution |
| Quality gate | One statistic of one metric (mean, harmonic mean, a percentile, minimum...), the 1st percentile of VMAF by default | Any combination of 8 statistics of VMAF, all having to pass, then the banding the encoder added (its mean, and its worst frame if asked) |
| Metrics | VMAF (a chroma-weighted variant included), SSIMULACRA2, Butteraugli, XPSNR | VMAF: the v1 models by default (luma and part of the color defects, see [what VMAF sees](#what-vmaf-sees-and-what-it-does-not)), without their banding term, and its banding feature, CAMBI, for the banding the encoder added (see [Fidelity and banding](#fidelity-and-banding)); the v0 models or any other model libvmaf knows when forced (see [Models](#models)) |
| Dial | The encoder's own quality setting: CRF (x264, x265, SVT-AV1), CQ level (aomenc, vpxenc), quantizer (rav1e) | Constant QP (see [why](#why-qp-instead-of-crf)) |
| Encoders | aomenc, SVT-AV1, rav1e, vpxenc, x264, x265 (their command line tools), with your own parameters | libx265, SVT-AV1 and hardware encoders (NVENC, VAAPI, D3D12VA, VideoToolbox) through ffmpeg, with fixed opinionated parameters |
| GPU | Decoding (DGDecNV), SSIMULACRA2 and Butteraugli (Vapoursynth-HIP) | Encoding (to search fast then encode the final file on CPU with `batchsearch --final-encode`, or as the final encoder) and decoding |
| Scene cuts | av-scenechange, cuts 24 frames apart at least, scenes longer than 10 s split further (defaults). Frame exact chunks piped from a VapourSynth source plugin, no intermediate file; without one, keyframe cuts into intermediate files (`hybrid`, can lose frames with open GOPs) or `select`, exact but decoding the whole source for every chunk | ffmpeg `scdet`, scenes shorter than 5 s (by default) merged into their shorter neighbor, no maximum length; frame exact cuts of a lossless intermediate (large: see [disk space](#disk-space)); a search of the scene threshold itself (`batchsearch`) |
| Verification | Frame count of every chunk; optional VMAF plot of the result | Frame counts of every segment and of the final file, final VMAF of the whole file embedded in its tags |
| Learning | None: every chunk starts its search from the middle of the quantizer range | QP statistics of the previous segments and encodes, to start the next searches closer |
| Interrupted run | Can be resumed | Starts over |
| Requirements | FFmpeg, VapourSynth (and a source plugin for frame exact chunks), the encoders' command line tools | ffmpeg (with libvmaf 3.2.0 or newer), ffprobe and mkvpropedit |

In short: choose Av1an to encode fast and well, with the encoder, the parameters and the metric of your choice, metrics seeing color (SSIMULACRA2, Butteraugli, XPSNR) included. Choose sptenc when the point is not to get close to a score but to never get under it, with several statistics at once, hardware encoders included, and to have the result written in the file.

### Why QP instead of CRF?

sptenc controls quality with **QP (Quantization Parameter)** in **CQP (Constant QP)** mode, not CRF.

This is not because CRF could not be searched: for a given segment, both dials are deterministic (same value, same file) and monotonic in practice (VMAF goes down as the value goes up: fidelity never rose with the QP on 20 real segments; the model's original score did on banded ones, see [Fidelity and banding](#fidelity-and-banding), and fidelity itself on smooth synthetic gradients costing a few hundred bytes per frame), which is all the interpolation search needs to converge in a few attempts. The reasons are elsewhere:

- **One dial for every encoder.** CRF is a software encoder concept. Hardware encoders expose a constant quantizer, or their own flavor of quality target, not CRF. With QP, the same search, the same statistics and the same workflow (search on a GPU encoder, final encode on its CPU counterpart) apply to every supported encoder.
- **No rate control competing with the search.** CRF is a rate control: the encoder moves bits between frames and blocks following its own perceptual model (adaptive quantization, cu-tree), which is not VMAF. sptenc already has something deciding where quality must vary, against the metric you chose: the scene splitter, then the search of each segment. A segment being cut on scene changes (with short scenes merged into a neighbor), its content is mostly homogeneous: there is not much left for a rate control to adapt to. Netflix's [Dynamic Optimizer](https://netflixtechblog.com/dynamic-optimizer-a-perceptual-video-encoding-optimization-framework-e19f1e3a277f) article makes the same point: *"Within a homogeneous set of frames, such as those that belong to the same shot, there is much less need to use rate-control, since very simple coding schemes, such as the fixed-quantization parameter ("fixed QP") mode, supported by virtually all existing video encoders, offers a very consistent video quality, with almost minimal bitrate variation."*

What happens around that base QP depends on the encoder. `libx265` turns adaptive quantization and cu-tree off by itself in constant QP mode, whatever is asked: the QP requested is the QP applied, frame type offsets aside. `libsvtav1` has its adaptive quantization off as well: ffmpeg's `-qp` sets its `aq-mode` to 0. NVENC encoders would honor their spatial and temporal adaptive quantization under `constqp`, the QP requested becoming a base the driver modulates per block: sptenc turns it off, and keeps their lookahead (see [Base ffmpeg encode options](#base-ffmpeg-encode-options)). VideoToolbox is driven through its quality setting, which its HEVC encoder turns into a constant QP, the same on every frame and without per-block changes, in coarser steps (see [its options](#hevc)). In every case these settings are **identical for every tested QP**, only the base QP moves, so the comparison between candidates remains stable.

Whether CRF would give a smaller or a bigger file at the same VMAF score depends on the content and is not something sptenc relies on. The guarantee does not come from the dial anyway: it comes from measuring every segment after it has been encoded, and encoding it again when it fails.
