# Split Encoder - Manual

Start with the [README](README.md). This is the "tell me everything" page.

1. [Input Requirements](#input-requirements)
2. [VMAF](#vmaf)
3. [Encoders](#encoders)
4. [Scene Detection and Threshold Selection](#scene-detection-and-threshold-selection)
5. [Adaptive QP Search](#adaptive-qp-search)
6. [Output](#output)
7. [Base ffmpeg encode options](#base-ffmpeg-encode-options)
8. [Installation details](#installation-details)
9. [Compared to other approaches](#compared-to-other-approaches)

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

## VMAF

### Models

By default, sptenc scores with the [VMAF v1 models](https://github.com/Netflix/vmaf/blob/master/resource/doc/models_v1.md), released by Netflix in June 2026 and built into libvmaf 3.2.0 and newer. Compared with the v0.6.1 generation, they add a banding feature ([CAMBI](https://github.com/Netflix/vmaf/blob/master/resource/doc/cambi.md)), a chroma one and an additive impairment term, and drop VIF. The enhancement gain the v0 NEG models removed is clamped in every v1 model: "NEG is enabled by default for VMAF v1 without a need for a separate model" ([Netflix](https://netflixtechblog.com/vmaf-v1-good-is-not-good-enough-60d7e4244ea8)).

| Model | Viewing condition | Scale | sptenc |
|---|---|---|---|
| `vmaf_v1.0.16_3d0h` | 1080p display, from 3 times its height | 0–100 | Selected below 2160 lines |
| `vmaf_v1.0.16_1d5h_2160` | 2160p display, from 1.5 times its height | 0–100 | Selected from 2160 lines |
| `vmaf_v1.0.16_5d0h` | Phone (1080p), from 5 times its height | 0–100 | Never selected, used with `--vmaf-model`: from that distance small artifacts go unseen, and they are what a quality floor is about |
| `vmaf_v1.0.16_3d0h_2160` | 2160p display, from 3 times its height | 0–110 | Never selected, used with `--vmaf-model`: same reason, and scores above 100 a threshold can not ask for |
| `_hfr` variants of the four | The same, for ~50/60 fps content | | Never selected, used with `--vmaf-model`: Netflix calls their frame rate handling "an area of active improvement" |

The model is **selected from the height of the input**: a 3840×1600 cropped film gets the 1080p model, a 3840×2160 one the 4K model. `--vmaf-model` forces another one, to judge a 1440p source as it would be seen on a 4K display for instance: a model made for another display than the source's is only a warning, contradicting the rule being the point of forcing it. It takes any model your libvmaf knows: the ones above, [the v0 ones](#the-v0-models), and models released after this version of sptenc, which it says it does not know (their viewing condition and scale are yours to check). The model is printed at the start of a run, written in the output tags (`sptenc_vmaf_model`) and part of the [QP cache](#persistent-stats-from-previous-runs) identity. Keep the viewing condition in mind when interpreting scores for other displays.

> **Requirements.** libvmaf 3.2.0 or newer. `encode`, `batchsearch` and `vmaf` load the model on two frames generated by ffmpeg before anything starts, and `sptenc check` lists the two models it selects with your libvmaf version. Once the source is known, and before its master is written, two frames of its own size are scored with the model of the run: below a minimum size libvmaf crashes, or writes no report while ffmpeg exits successfully, and that minimum depends on the model and on the shape of the picture. Measured with a libvmaf of September 2026: the two models sptenc selects score 216×160 but not 1920×160, the phone ones need 480×270 and the 4K ones at 3 times the height 568×320 (at 16:9).

#### The v0 models

sptenc v0.1.0 scored with the previous generation, the v0.6.1 models. libvmaf still builds them in and `--vmaf-model` still takes them, but sptenc never selects them: **they measure luma only, and do not see banding** (their NEG variants barely do). [What VMAF sees](#what-vmaf-sees-and-what-it-does-not) measures each of these defects with both generations. A run forcing one warns about it, and prints no banding line under its VMAF tables: v0 has no banding feature.

| Model | Viewing condition |
|---|---|
| `vmaf_v0.6.1` | 1080p display, from 3 times its height |
| `vmaf_v0.6.1neg` | The same, with No Enhancement Gain: enhancements such as sharpening can not raise the score ([NEG mode](https://github.com/Netflix/vmaf/blob/master/resource/doc/models_v0.md#disabling-enhancement-gain-neg-mode)) |
| `vmaf_4k_v0.6.1` | 2160p display, from 1.5 times its height |
| `vmaf_4k_v0.6.1neg` | The same, with No Enhancement Gain (built into libvmaf 3.0 and newer) |

Forcing one is for comparisons: scoring the way the [published anchors](#for-reference-the-v0-anchors) were measured, or the way sptenc v0.1.0 did (the QP statistics it gathered with that model are then reused, see [the cache](#persistent-stats-from-previous-runs)). v0.1.0 could also compute them on an NVIDIA GPU: see [why VMAF now runs on the CPU](#vmaf-runs-on-the-cpu).

### Quality Score Reference

VMAF scores are relative to the viewing conditions the models were trained on (1080p display at 3 times its height; 4K display at 1.5 times its height), so treat them as comparable only within the same viewing context.

No subjective study has been published on the v1 scale yet. Netflix built it to read like v0's: "we calibrated the VMAF v1 scale to align with v0 via a score transform, so that the new algorithm preserves the meaning of the numbers" ([Netflix](https://netflixtechblog.com/vmaf-v1-good-is-not-good-enough-60d7e4244ea8)). The published anchors below, established with the v0 models, therefore remain the reference to pick a first value, and sptenc's defaults come from them. Where v1 departs from them on real content is [measured below](#v1-against-v0-on-real-content).

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

#### v1 against v0 on real content

The same content cut in the same segments, encoded with `hevc_nvenc` behind the same gate (harmonic mean 93), once scored with v0.6.1 (sptenc v0.1.0) and once with v1, then each result scored with the other model:

| Content | Video stream, v0 → v1 | Mean QP, v0 → v1 | v1 score of the v0 encode | v0 score of the v1 encode |
|---|---|---|---|---|
| 26 min anime episode, 1080p, dark gradients (163 segments) | 175.0 → 283.4 MiB (+62%) | 26.6 → 23.5 | 91.0 | 95.0 |
| <!-- TODO(night): movie --> Film, TBD | TBD | TBD | TBD | TBD |

Mean QP is weighted by the frame count of each segment, scores are harmonic means. On the episode, v1 lowered the QP of 153 segments out of 163 (by up to 12), raised it on 7, and no segment ended as best effort: behind the same number, v1 was as demanding as v0 at 95. Content showing the defects v0 did not see (see [below](#what-vmaf-sees-and-what-it-does-not)) is where v1 is expected to depart from v0's scale; which of its features made the difference on this episode was not measured.

The defaults and the values recommended below still come from the v0 anchors. sptenc promises a quality floor, and a stricter floor is the side to err on: if yours proves more demanding than you need, encode a few segments of your content at a lower value, look at them, and lower the gate.

### What VMAF sees, and what it does not

Every guarantee sptenc makes is a guarantee on a VMAF score: it is worth what the metric is worth. VMAF is a good predictor of perceived quality for what it was built for, compression and scaling artifacts on the picture structure. The v1 [models](#models) see defects the v0.6.1 ones were blind to, not all of them. Measured with the `vmaf` command on synthetic 10-bit 1080p clips (`testsrc2` and a dark gradient from ffmpeg's `lavfi`), scores given as v0.6.1 / v0.6.1 NEG / v1:

- **Color, partly.** v0 only measures luma. v1 has a chroma feature: a copy with its hues rotated by 90° scores 65.9 with v1 (99.6 / 99.6 with v0, the score of the untouched copy), one with heavily blurred chroma 66.0. But a fully desaturated (grayscale) copy still scores 100 with v1: the chroma feature reacts to changes within the chroma planes, not to the color being gone. In practice encoders quantize luma and chroma together, so a segment passing is not expected to be damaged on chroma alone: this is an expectation, not something sptenc verifies.
- **Banding, seen.** A smooth dark gradient reduced to 18, 10 and 5 luma levels scores 100 at every step with v0 (the added edges count as an enhancement), 96 with its NEG variant, and 91.8, 86.0 and 76.5 with v1. v1 also fuses Netflix's banding detector, [CAMBI](https://github.com/Netflix/vmaf/blob/master/resource/doc/cambi.md), into the score. CAMBI looks for fine staircases in smooth areas: a gradient in 8-bit steps reaches its ceiling of 17, while coarse posterization like the one above leaves it at 0 (the other features catch it). sptenc prints CAMBI's mean and maximum under every VMAF table (0 is no banding, and "a CAMBI score around 5 is where banding starts to become slightly annoying"): it is part of the score already, never a gate. The mandatory 10-bit output is still there to avoid creating banding in the first place.
- **Banding in the source counts too.** CAMBI rates the encoded picture alone, not what the encoder changed: a picture that is banded already is marked down even compared with itself. Compared with itself, a still picture scores 100 with v1, a smooth 10-bit dark gradient 95.9 (CAMBI rates it 6.7). The ceiling of such a segment is below 100, and a gate above its ceiling can only end in best effort. On the anime episode [measured above](#v1-against-v0-on-real-content), no segment did at 93.
- **What happens between frames.** Frames are scored one by one, then the scores are pooled. A quality changing from a frame to the next (flicker, keyframe pulsing) is not judged as such, only the score of each frame is. The [VMAF FAQ](https://github.com/Netflix/vmaf/blob/master/resource/doc/faq.md) itself mentions psycho-visual evidence that viewers weigh the worst frames more heavily than an arithmetic mean does: this is what the percentile and minimum gates are for.
- **HDR.** Netflix has not released an HDR model, v1 included: "We also plan to release an HDR version enhanced by the v1 improvements" ([Netflix](https://netflixtechblog.com/vmaf-v1-good-is-not-good-enough-60d7e4244ea8)). sptenc computes VMAF on HDR sources as on any other, without any tone mapping: take these scores as an indication, not as a validated measure.

If your content is exposed to one of these (banded sources, flicker, HDR), have a look at a few segments yourself before trusting the numbers and deleting a source.

### sptenc thresholds

VMAF scores range from 0 to 100 with the models sptenc offers. A difference of about 6 points is often quoted as one Just Noticeable Difference (JND): a rule of thumb from a contact at Netflix ([quoted by Ozer](https://streaminglearningcenter.com/codecs/finding-the-just-noticeable-difference-with-netflix-vmaf.html)), established with the v0 models, not a threshold measured on your content.

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

> 💡 **Tip:** run a first encode with `--debug` to see the statistics of every attempt, segment by segment: the segments whose p5 or min fall far below their mean are the ones a tail gate would act on. On a segment of fewer than 150 frames, p1 is its worst frame, the same as min (at the 5 s [minimum segment length](#the---min-segment-length-guardrail), 24 and 25 fps segments have 120 and 125 frames).

### Recommended Values

These values come from the [v0 anchors](#for-reference-the-v0-anchors): on content where v1 sees more than v0 did, they are stricter than they read (see [v1 against v0](#v1-against-v0-on-real-content)).

| Use Case | Gate | Target value |
|---|---|---|
| "I am afraid of deleting my lossless master file" | `--vmaf-mean` + `--vmaf-min` | `99` + `93` ¹ |
| Archival / mastering | `--vmaf-hmean` | `95` |
| General viewing, streaming, VOD | `--vmaf-hmean` | `93` (default) ² |
| Mobile / bandwidth-constrained | `--vmaf-hmean` | `85–90` |
| Quality consistency critical | `--vmaf-mean` + `--vmaf-p5` | `93` + `90` ¹ |

¹ With `--vmaf-hmean -1`: the tail gate bounds the bad frames, the mean sets the level (see above).

² fast-motion content needs no dedicated profile: motion-heavy segments fail the gate and converge to a lower QP automatically.

> **93 vs 95?** The 93 target comes from Rassool (RealNetworks, [IEEE BMSB 2017](https://doi.org/10.1109/BMSB.2017.7986143), [PDF](https://realnetworks.com/sites/default/files/vmaf_reproducibility_ieee.pdf)), who found that encoding to about 93 would serve the vast majority of viewers with content *"either indistinguishable from original or with noticeable but not annoying distortion"* (the two best ratings of the 5-level scale of his test, on 4K clips). The 95 target comes from Kah et al. ([Proc. SPIE 11842, Applications of Digital Image Processing XLIV, 2021](https://doi.org/10.1117/12.2593952)): VMAF 95 is the lowest score *"at which a video signal is on average subjectively indistinguishable from the original video signal"* (ITU-R BT.500 subjective tests on a 4K OLED TV viewed from twice its height), a deliberately higher bar. In Ozer's test on the *Meridian* clip, choosing 95 over 93 cost about 1400 kbps on the top rung (clip-specific, not universal). The default targets the first; the jump to 95 remains an explicit opt-in. Both studies used the v0 models: on the anime episode [measured above](#v1-against-v0-on-real-content), the v1 gate at 93 already gave an encode v0 scores at 95.

### VMAF runs on the CPU

sptenc v0.1.0 could compute VMAF on an NVIDIA GPU (`--vmaf-cuda`, with `libvmaf_cuda`). The v1 models can not run there: libvmaf's CUDA code only covers the features of the v0 models, and loading a v1 model in `libvmaf_cuda` fails on its banding feature (`could not initialize feature extractor "Cambi_feature_cambi_score"`). VMAF is computed on the CPU, whatever the encoder; the GPU can still decode its inputs (see [Hardware decoding](#hardware-decoding)).

That is the price of seeing banding and color. On the episode of the [Encoders](#encoder-selection-vs-file-size) section, searched with `hevc_nvenc` and `-C 6`, the search took 11m13s with VMAF v0 on the GPU, 16m5s with v0 on the CPU and 17m10s with v1 on the CPU. v1 itself costs less CPU than v0 (libvmaf spent 0.040 CPU-seconds per 1080p frame against 0.067), but decoding the lossless intermediate twice per attempt costs the search more than VMAF does (0.03 to 0.05 CPU-seconds per frame each time), and v1 does not change that.

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
| Output file size | ✅ Smallest | ❌ Larger: <!-- TODO(night): libx265 run --> TBD× on the episode measured below |
| Speed | Hours for a film | ✅ Several times faster: 26 min of 1080p searched in 17 minutes below, with several segments searched in parallel (`-C`, see below) |
| Recommended for | Final archival encode | VMAF profile prototyping, split threshold value search, and final encodes when time matters more than size (the CPU encode was TBD smaller below) |

Same 26 min 1080p Blu-ray remux episode, same threshold (163 segments), cold cache, VMAF harmonic mean 93, on an RTX 5090 with a 16 cores / 32 threads CPU:

| | `hevc_nvenc`, `-C 6` | `libx265` preset slow, `-C 3`, decoding on NVDEC |
|---|---|---|
| QP search | 17m10s | <!-- TODO(night): libx265 run --> TBD |
| Video stream | 283.4 MiB | TBD |
| Attempts per segment | 3.99 | TBD |

One episode is one data point: the size ratio depends on the content, and the times on the machine. On this one, the GPU search was limited by the CPU (busy 81% of the time), which decodes the lossless FFV1 intermediate twice per attempt (once for the encode, once as the VMAF reference) and computes VMAF, not by the GPU (NVENC busy 24% of the time): a smaller CPU would have made it slower, smaller GPUs were not measured.

> **Concurrent segments (`-C`)**: segments are searched one at a time by default. The output is the same whatever the value, only the time it takes changes.
>
> **With a GPU encoder**, the number of encoding engines on the card is not the limit: a worker only feeds the encoder while it encodes and waits for VMAF the rest of the time, and the frames come from the CPU decoding the FFV1 intermediate, as measured above. On the RTX 5090 (three NVENC engines), `-C 6` kept the encoders busy 24% of the time and the CPU 81%: raise it until the CPU is saturated, the driver's encode session limit being the hard stop (an encode then fails to open its session).
>
> **With a CPU encoder**, a single encode already uses every thread of the machine, but does not keep a many-core CPU fully busy: on a 16 cores / 32 threads CPU, `libx265` at 1080p encoded 26% more frames per second with 2 concurrent segments (measured with VMAF v0 on the CPU). Expect less with fewer cores or bigger pictures (+21% with 2 encodes at 2160p), mind the memory with 4K content, and measure on your machine.
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
- **MPEG-1, MPEG-2, MPEG-4 Part 2 and MJPEG sources are always decoded by the CPU** (DVDs, older captures), whatever the encoder and the flags. Their decoded pixels depend on the inverse DCT of the decoder, whose error the MPEG-1, MPEG-2 and JPEG standards only bound: NVDEC's frames differed from ffmpeg's on every frame of a test clip of each (PSNR 60 to 66 dB), and ffmpeg's own inverse DCTs do not agree either. Invisible, but the master, and the output with it, would not be the one a software decode gives.
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

The `--min-segment-length` flag (alias `-L`, default 5s) removes boundaries that would create segments shorter than the given duration. This is a quality-floor guardrail: it prevents unreliable percentile metrics and B/P-frame starvation by merging short segments into their shorter neighbour.

It does **not** protect against the opposite problem. Segments longer than ~5 seconds may still be too long for your tolerance of the drowning risk. That judgment remains yours.

Use the `thresholds` command to preview the segment distributions a threshold would produce before committing to an `encode` or a `batchsearch` run. It is fast and produces no files.

### Reusing a threshold

`encode`, `thresholds`, and `batchsearch` all build scenes the same way: the threshold picks the boundaries first, then `--min-segment-length` merges the short segments that remain. The scenes a threshold produces therefore depend only on that threshold and on `--min-segment-length` — not on the `--min-threshold`/`--max-threshold` range a search was run with.

This makes thresholds portable: a row of the `thresholds` table, or the best candidate reported by `batchsearch`, gives exactly the same scenes when passed to `encode -T` with the same `--min-segment-length`. A typical use is to run `batchsearch` on one episode and `encode -T <best>` on the rest of the season.

Reported thresholds look like `24.2765` rather than `24.277`: ffmpeg prints scene scores rounded to 3 decimals but compares thresholds against the unrounded score, so sptenc reports half a step below the printed score to guarantee the boundary is kept. Use the value as printed.

## Adaptive QP Search

sptenc's per-segment QP search uses a 3-step algorithm that converges on the highest valid QP (smallest file) efficiently, even on the first run:

1. **Smart start** — The first candidate is the mean QP of the segments already done in this run, seeded with the mean of previous runs (see [below](#persistent-stats-from-previous-runs)): the first segment starts from that seed, whose weight then fades as segments complete. Without any previous run, the seed is the midpoint of the encoder's QP range (e.g. QP 26 for libx265's 0–51 range).
2. **Bracketing** — Steps away from the starting point, one standard deviation further at each encode, to find one valid QP (passes VMAF) and one invalid QP (fails VMAF), closing the search range around the boundary. The standard deviation comes from the same statistics as the mean; without any previous run, it starts at a quarter of the QP range (e.g. 13 for libx265's 0–51 range).
3. **Interpolation** — Once bracketed, **Fritsch-Butland monotone cubic interpolation** forecasts the VMAF of the untested QPs within the range and picks the next one to encode, walking toward the highest valid QP without blind probing. A forecast only chooses the next encode: every QP kept was encoded and measured, and the next higher one encoded and found failing.

This typically takes 3 to 5 encodes per segment (4.0 on average with `hevc_nvenc` on the [episode measured](#encoder-selection-vs-file-size)<!-- TODO(night): and X with libx265 -->), where a plain bisection of libx265's 0–51 range takes about 6 whatever the content.

### Persistent stats from previous runs

After each encode job finishes, sptenc stores QP statistics **per encoder, VMAF model and VMAF profile** (i.e. the combination of the encoder, the 1080p or 4K model, the enabled metrics, and their target values), weighted by the number of segments. A `batchsearch` run only stores its winning candidate (plus the final CPU encode with `--final-encode`, on that encoder's own cache): the other candidates are the same content encoded again and would over-represent that file. Within the run though, every candidate starts from the QPs the previous candidates found: the same content is the best prior there is, so only the first candidate pays the learning cost.

These statistics only seed a search. In the run's own statistics they weigh a single segment, however many files they aggregate, so the file being encoded takes over within a handful of segments: a cache built on other content sets the starting point, it does not hold the search to its mean.

What is at stake is the number of encodes per segment. On the episode of the [Encoders](#encoder-selection-vs-file-size) section, a cold cache cost 4.0 attempts per segment with `hevc_nvenc`<!-- TODO(night): and X with libx265 -->, the run's own ephemeral statistics doing the learning from the first segments on. A segment can not take fewer than two, the QP kept and the next one failing, so what a warm cache can save is bounded by that: it has not been measured on that content yet.

The cache provides:
- **Mean QP** — a better-informed starting point than the encoder midpoint
- **Standard deviation** — a tuned step size for bracketing, rather than a heuristic fraction of the range

The stats files are **profile-specific**: changing the encoder, the VMAF model (`--vmaf-model`, or a 4K source) or any VMAF threshold value starts a fresh learning curve. The statistics of sptenc v0.1.0 were gathered with the v0 models, whose names are in their file names: later versions only read them again for a run forcing the same v0 model with `--vmaf-model`. `sptenc cache` lists them under their v0 model name, and deletes them by index.

### Cache isolation with profiles

Because a given VMAF target can require very different QP distributions depending on the source (e.g. clean animation vs. grainy film), mixing them into the same cache effectively poisons it. Use `--cache-profile <name>` (e.g. `pixar_animation`, `grainy_90s`) to keep these histories separate. Without a profile, all runs with the same encoder, VMAF model and VMAF profile share the same cache.

| Flag | Short | Default | Description |
|---|---|---|---|
| `--stats-cache-dir` | `-s` | OS cache dir (`~/.cache/sptenc` or equivalent) | Directory where QP statistics are stored |
| `--cache-profile` | `-c` | *(none)* | Isolate cache history between content types |

## Output

The output is always Matroska (`.mkv`) because it is the most permissive container for stream copy.

Every stream of the source other than video (audio, subtitles, attachments, data) is copied into the output. When every audio track is 16 or 24-bit little-endian PCM (`pcm_s16le`, `pcm_s24le`, as in MKV remuxes of Blu-ray discs), they are losslessly compressed to FLAC; otherwise audio is copied as is.

Color metadata (`colorspace`, `color_trc` and `color_primaries`) is probed from the source and re-injected into the output container. The color range (`color_range`) is the exception: the output declares the range of the encoded video, and a full range ("PC") source comes out in limited range ("TV"), converted when the master is written (see [Base ffmpeg encode options](#base-ffmpeg-encode-options)). Tested with libx265, hevc_nvenc, libsvtav1 and av1_nvenc, on 8-bit and 10-bit full range sources. HDR metadata handling is still being validated.

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

## Base ffmpeg encode options

These are the opinionated defaults sptenc passes to ffmpeg. They are intentionally not configurable: the goal is to let you tune **VMAF thresholds** and **scene detection**, not encoder minutiae. If you need full control over every ffmpeg flag, ffmpeg itself is the right tool.

The defaults are selected for a single goal: **a VMAF target met by every segment, at the smallest file size**. Every option is chosen with that trade-off in mind.

> **10-bit output is mandatory.** Every encoder gets 10-bit 4:2:0 frames (`main10` for HEVC, `main` for AV1 which includes 10-bit): `yuv420p10le` for the CPU encoders, and `p010` for the hardware ones, the layout their APIs require (the same samples, with the chroma planes interleaved and the values stored in the high bits). Repacking one into the other is lossless: checked frame by frame for the CUDA and software conversions sptenc uses (VA-API converts on the GPU, not checked). 10-bit greatly reduces banding and improves compression efficiency at low bitrates — it is the modern baseline for quality encoding.
>
> The lossless FFV1 master is already stored as `yuv420p10le`. From a limited range ("TV") 8-bit or 10-bit 4:2:0 source, by far the most common kind, it holds every sample of the source without loss (8-bit values are shifted to 10 bits, exactly). Other sources are converted at that step, as the final encode would require anyway: 4:2:2 and 4:4:4 ones get their chroma subsampled (luma stays exact, and VMAF compares the encodes with this master: the chroma resolution lost here is not something it can see), sources deeper than 10 bits are reduced to 10 bits, and full range ("PC") sources are converted to limited range: without loss from 8 bits (each 8-bit level gets a 10-bit level of its own), with a slight loss from 10 bits (the 1024 luma levels of the source share the 877 of the limited range).

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
ffmpeg [...] -c:v 'hevc_videotoolbox' -profile:v 'main10' -q:v '101-X' -bsf:v 'dump_extra' [...]
```

VideoToolbox has no constant QP mode: sptenc drives its constant quality setting instead, from 1 (worst) to 100 (best), inverted so that the search sees a QP-like scale (X from 1 to 100, higher meaning smaller and worse). `dump_extra` repeats the parameter sets before every keyframe: this encoder writes the quality into them, and without it the segments put back together would be decoded with the parameters of the first one.

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
| Quality gate | One statistic of one metric (mean, harmonic mean, a percentile, minimum...), the 1st percentile of VMAF by default | Any combination of 8 statistics of VMAF, all having to pass |
| Metrics | VMAF (a chroma-weighted variant included), SSIMULACRA2, Butteraugli, XPSNR | VMAF only: the v1 models by default (luma, banding and part of the color defects, see [what VMAF sees](#what-vmaf-sees-and-what-it-does-not)), the v0 ones or any other model libvmaf knows when forced (see [Models](#models)) |
| Dial | The encoder's own quality setting: CRF (x264, x265, SVT-AV1), CQ level (aomenc, vpxenc), quantizer (rav1e) | Constant QP (see [why](#why-qp-instead-of-crf)) |
| Encoders | aomenc, SVT-AV1, rav1e, vpxenc, x264, x265 (their command line tools), with your own parameters | libx265, SVT-AV1 and hardware encoders (NVENC, VAAPI, D3D12VA, VideoToolbox) through ffmpeg, with fixed opinionated parameters |
| GPU | Decoding (DGDecNV), SSIMULACRA2 and Butteraugli (Vapoursynth-HIP) | Encoding (to search fast then encode the final file on CPU with `batchsearch --final-encode`, or as the final encoder) and decoding |
| Scene cuts | av-scenechange; chunks decoded frame exact through a VapourSynth source plugin (BestSource, L-SMASH, FFMS2 or DGDecNV), no intermediate file; without one, cuts on keyframes into intermediate files | ffmpeg `scdet`, frame exact cuts of a lossless intermediate (large: count on disk space), and a search of the scene threshold itself (`batchsearch`) |
| Verification | Frame count of every chunk; optional VMAF plot of the result | Frame counts of every segment and of the final file, final VMAF of the whole file embedded in its tags |
| Learning | None: every chunk starts its search from the middle of the quantizer range | QP statistics of the previous segments and encodes, to start the next searches closer |
| Interrupted run | Can be resumed | Starts over |
| Requirements | FFmpeg, VapourSynth (and a source plugin for frame exact chunks), the encoders' command line tools | ffmpeg (with libvmaf 3.2.0 or newer), ffprobe and mkvpropedit |

In short: choose Av1an to encode fast and well, with the encoder, the parameters and the metric of your choice, metrics seeing color (SSIMULACRA2, Butteraugli, XPSNR) included. Choose sptenc when the point is not to get close to a score but to never get under it, with several statistics at once, hardware encoders included, and to have the result written in the file.

### Why QP instead of CRF?

sptenc controls quality with **QP (Quantization Parameter)** in **CQP (Constant QP)** mode, not CRF.

This is not because CRF could not be searched: for a given segment, both dials are deterministic (same value, same file) and monotonic in practice (VMAF goes down as the value goes up; no exception has been observed), which is all the interpolation search needs to converge in a few attempts. The reasons are elsewhere:

- **One dial for every encoder.** CRF is a software encoder concept. Hardware encoders expose a constant quantizer, or their own flavor of quality target, not CRF. With QP, the same search, the same statistics and the same workflow (search on a GPU encoder, final encode on its CPU counterpart) apply to every supported encoder.
- **No rate control competing with the search.** CRF is a rate control: the encoder moves bits between frames and blocks following its own perceptual model (adaptive quantization, cu-tree), which is not VMAF. sptenc already has something deciding where quality must vary, against the metric you chose: the scene splitter, then the search of each segment. A segment being cut on scene changes (with short scenes merged into a neighbor), its content is mostly homogeneous: there is not much left for a rate control to adapt to. Netflix's [Dynamic Optimizer](https://netflixtechblog.com/dynamic-optimizer-a-perceptual-video-encoding-optimization-framework-e19f1e3a277f) article makes the same point: *"Within a homogeneous set of frames, such as those that belong to the same shot, there is much less need to use rate-control, since very simple coding schemes, such as the fixed-quantization parameter ("fixed QP") mode, supported by virtually all existing video encoders, offers a very consistent video quality, with almost minimal bitrate variation."*

What happens around that base QP depends on the encoder. `libx265` turns adaptive quantization and cu-tree off by itself in constant QP mode, whatever is asked: the QP requested is the QP applied, frame type offsets aside. `libsvtav1` has its adaptive quantization off as well: ffmpeg's `-qp` sets its `aq-mode` to 0. NVENC encoders keep their spatial and temporal adaptive quantization (and their lookahead) active under `constqp`: the QP requested is a base the driver modulates per block. VideoToolbox has no constant QP at all: sptenc drives its constant quality setting (see [its options](#hevc)). Either way these settings are **identical for every tested QP**, only the base QP moves, so the comparison between candidates remains stable.

Whether CRF would give a smaller or a bigger file at the same VMAF score depends on the content and is not something sptenc relies on. The guarantee does not come from the dial anyway: it comes from measuring every segment after it has been encoded, and encoding it again when it fails.
