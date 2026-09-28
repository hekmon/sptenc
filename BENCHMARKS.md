# Split Encoder - Benchmarks

The measurements behind the quality, banding, search time, file size and disk space figures of the [README](README.md) and the [manual](MANUAL.md). One machine, a Mac for VideoToolbox, two contents, synthetic clips: they tell what happened there, not what will happen on yours.

1. [Setup](#setup)
2. [Gates on the two contents](#gates-on-the-two-contents)
3. [Search time](#search-time)
4. [Encoders](#encoders)
5. [NVENC adaptive quantization](#nvenc-adaptive-quantization)
6. [VideoToolbox](#videotoolbox)
7. [Banding](#banding)
8. [Synthetic clips](#synthetic-clips)
9. [Minimum picture sizes](#minimum-picture-sizes)

## Setup

- **Machine:** AMD Ryzen 9 9950X3D (16 cores, 32 threads), NVIDIA RTX 5090, Linux under WSL2 on Windows. ffmpeg n9.0.2 with libvmaf f85a8536 (a September 2026 build).
- **Contents:** two 8-bit 1080p Blu-ray remuxes at 23.976 fps. A 26 min anime episode with dark gradients (37,393 frames, cut in 163 segments at threshold 8.0055) and a 101 min live-action film (145,397 frames, 225 segments at threshold 10), both with the default 5 s minimum segment length. Every run of a content encoded the very same segments. Source files of 4.04 and 23.77 GiB, lossless FFV1 masters of 14.29 and 59.30 GiB (33.0 and 35.2 GiB per hour).
- **Runs:** `hevc_nvenc` with `-C 6` (`libx265` with `-C 3` in [Encoders](#encoders)), a cold cache for each run (an empty cache directory), a harmonic mean gate. sptenc v0.1.0 for the v0 runs, development builds of v0.2.0 for the others. NVENC ran with its adaptive quantization on, which sptenc has since turned off, except in the runs labeled AQ off (see [NVENC adaptive quantization](#nvenc-adaptive-quantization)).
- **Scores of the outputs:** each output scored against its source (or its lossless master, the same pictures), the whole file at once, then split back into its segments.
- **Durations** are wall clock times, read from the timestamps of the logs. The Linux kernel of that machine had its clock tick adjusted, running from 3% slow to 10% fast depending on the day, and every duration measured on it ran with it, the ones sptenc prints included: only the timestamps follow the host's clock.

## Gates on the two contents

The v0.6.1 runs are sptenc v0.1.0. "v1 original" gates on the score of `vmaf_v1.0.16_3d0h` as the model computes it, CAMBI included (`--vmaf-original`), "fidelity" on the same model with its CAMBI term set to zero, the default (see [Fidelity and banding](MANUAL.md#fidelity-and-banding)). Mean QP is weighted by the frame count of each segment, scores are harmonic means over the whole file, an empty cell a score that was not measured.

**Anime episode**

| Gate | Video stream | Against v0.1.0 | Mean QP | Attempts per segment | Best effort | v0.6.1 | v1 original | Fidelity |
|---|---|---|---|---|---|---|---|---|
| v0.6.1 at 93 | 175.0 MiB | | 26.6 | 3.79 | 0 | 93.40 | 90.99 | |
| v1 original at 93 | 283.4 MiB | +62% | 23.5 | 3.99 | 0 | 95.03 | 93.31 | |
| v1 original at 95 | 845.4 MiB | +383% | 19.1 | 4.33 | 2 | 96.44 | 95.21 | 96.72 |
| Fidelity at 93 | 200.9 MiB | +15% | 25.8 | 3.72 | 0 | 94.05 | 92.02 | 93.36 |
| Fidelity at 95 | 299.8 MiB | +71% | 22.7 | 3.88 | 0 | 95.51 | 93.89 | 95.29 |
| Fidelity at 93, AQ off: the default | 176.5 MiB | +1% | 24.4 | 3.92 | 0 | 94.32 | 92.15 | 93.43 |

**Live-action film**

| Gate | Video stream | Against v0.1.0 | Mean QP | Attempts per segment | Best effort | v0.6.1 | v1 original | Fidelity |
|---|---|---|---|---|---|---|---|---|
| v0.6.1 at 93 | 1687.7 MiB | | 24.5 | 3.70 | 0 | 93.40 | 90.13 | |
| v1 original at 93 | 3387.1 MiB | +101% | 21.2 | 3.70 | 0 | 95.62 | 93.42 | |
| v1 original at 95 | 6372.0 MiB | +278% | 18.3 | 3.89 | 2 | 96.97 | 95.36 | 96.29 |
| Fidelity at 93 | 2587.6 MiB | +53% | 22.5 | 3.54 | 0 | 94.96 | 92.42 | 93.48 |
| Fidelity at 95 | 4440.9 MiB | +163% | 19.9 | 3.67 | 0 | 96.39 | 94.51 | 95.42 |
| Fidelity at 93, AQ off: the default | 1820.4 MiB | +8% | 20.7 | 3.78 | 0 | 95.14 | 92.11 | 93.37 |

The default of v0.2.0, fidelity at 93 with the CAMBI gate at a mean of 1, gave the output of fidelity at 93 on both contents with AQ on, packet for packet: the gate lowered no segment. With AQ off, it lowered none either (see [NVENC adaptive quantization](#nvenc-adaptive-quantization)).

Per segment, at 93:
- **v1 original against v0.1.0:** v1 lowered the QP of 153 segments out of 163 on the episode (by up to 12) and raised it on 7, of 216 out of 225 on the film (by up to 11) and raised it on one. Most lost 2 to 5 QP (69% of the episode's segments, 80% of the film's), about 3 on average on both. The segments that lost exactly 3 grew 1.44 times on the episode, 1.93 times on the film.
- **Fidelity against v0.1.0:** lower on 108 segments of the episode, equal on 33, higher on 22 (from 5 below to 7 above); lower on 195 of the film, equal on 25, higher on 5 (from 6 below to 2 above). v0 scores 93 or more the segments holding 83.5% of the episode's frames and 98.9% of the film's. Below it on the episode: the end credits, text on black, where v1 is more lenient than v0 (v0 88.0 to 91.2), and 17 segments within 0.9 of it.
- **Best efforts:** the two segments per content that no QP could pass with the original score at 95 peak at 94.05 to 94.94, their lowest QP scoring below the peak (see [the banded sources](#sources-against-themselves)). Fidelity at 95 has none.

At 95, v0 scores 95 or more the segments of fidelity holding 65.1% of the episode's frames (117 segments out of 163) and 93.8% of the film's (208 out of 225).

The master profile of the manual, a mean of 99 and a minimum of 94, estimated on the 20 segments encoded every 2 QPs from 0 to 40 for [the banding survey](#sources-against-themselves), fidelity's harmonic mean standing for its mean (it is never higher): at the highest QP reaching 99, the worst frame is at 95.1 to 98.6 on 19 segments, and at 91.7 on the last one, which a minimum of 93 takes 2 QPs lower and one of 94 4 QPs lower. Over the 20 segments, a minimum of 93 adds 0.7% to the size a mean of 99 gives alone, one of 94 1.7%. At those QPs, they add 0.009 of banding at most on average over their frames (0.78 on the worst frame): no CAMBI mean threshold from 0.1 to 1 lowers any of them (NVENC ran with AQ on). With AQ off, the film segment that adds 0.80 at the default adds nothing from QP 14 down, and this profile keeps QP 8 for it. On the [smooth gradients](#the-cambi-gate-on-smooth-gradients), a mean of 0.25 takes the `hevc_nvenc` ramp from QP 19 (0.39) to 18 (0.24), where 0.5 keeps 19, and both take the `libx265` clean sky, which adds 1.12 at QP 14, down to QP 8 (0.06, 1.11 times the size, sampled every 2 QPs); the grainy sky and the vignette reach fidelity 99 at no QP, with `hevc_nvenc` as with `libx265`.

## Search time

The search alone, wall clock, `hevc_nvenc -C 6`, harmonic mean 93, AQ on but in the last row:

| VMAF | Episode | Film | CPU busy (episode, film) |
|---|---|---|---|
| v0.6.1 on the GPU, `libvmaf_cuda` (sptenc v0.1.0) | about 11m35s ¹ | | |
| v0.6.1 on the CPU (sptenc v0.1.0) | 16m35s | 1h2m53s | 87%, 86% |
| v1 original | 17m46s | 1h3m19s | 81%, 79% |
| Fidelity | 16m19s ² | 1h4m24s | 81%, 78% |
| Fidelity and the CAMBI gate | 18m20s, 18m51s ³ | 1h12m2s, 1h13m41s ³ | 83%, 79% |
| Fidelity, CAMBI measured in every attempt ⁴ | 19m18s | 1h17m29s | 87%, 82% |
| Fidelity and the CAMBI gate, AQ off: the default | 19m10s | 1h13m41s | 85%, 82% |

¹ Its log has no timestamps: sptenc printed 11m13s, corrected by the ratio of that run's printed total to its wall clock total (0.968).
² Two runs, in the evening then at night: 16m22s and 16m16s, the same QPs on every segment and the same video.
³ Two runs, a development build at night, then the implementation in the evening: the same ffmpeg commands (compared with `--debug` on a synthetic clip), the same QPs on every segment and the same video.
⁴ Not what sptenc does: the CAMBI gate measures the banding once per segment, at the QP the VMAF search found (see [Fidelity and banding](MANUAL.md#fidelity-and-banding)).

Measured once per segment, the CAMBI gate added 12 to 15% to the search time and 14 to 17% to the CPU time, depending on the runs compared. On the episode, 12.7% and 14.2% against the fidelity run of the same night, 15.2% and 17.2% for the evening run against the evening fidelity run of the day before. On the film, 14.4% and 16.3% evening against evening (11.9% and 14.2% for the night run against the evening fidelity run). The same work does not always take the same time on that machine: the evening runs of the default took 2.8% (episode) and 2.3% (film) longer than the night ones, and the evening fidelity run of the episode 0.6% longer than the night one (up to about 3% along the way). Measured in every attempt, the banding added 18.6% to the search time and 26.0% to the CPU time of the episode, against the fidelity run of the same night, and 20.3% and 27.3% on the film (its morning run against the evening fidelity run). On each content, the fidelity runs above, with and without the CAMBI gate, gave the same QP on every segment and packet-identical video streams, and both ways of measuring the banding the same values at the QPs kept. NVENC was busy 21 to 26% of the time in every run computing VMAF on the CPU (37% with `libvmaf_cuda`): the CPU is what limits a GPU search, decoding the lossless intermediate twice per attempt (once for the encode, once as the VMAF reference) and computing VMAF.

Where that CPU time goes, per 1080p frame, on a segment of the episode (886 frames, both inputs decoded beforehand, 16 threads, the mean of 3 runs): libvmaf spent 0.056 CPU-seconds with v0.6.1, 0.036 with the v1 original score, 0.035 with fidelity and 0.020 on the added banding alone. Decoding the FFV1 intermediate took 0.030 (0.046 with 12 segments decoded at once), and an attempt decodes it twice: more than VMAF itself.

## Encoders

The episode (the film was not encoded with `libx265`), `hevc_nvenc` with `-C 6` and `libx265` with `-C 3` (decoding on NVDEC), at sptenc's presets (p7 and slow), at the default (fidelity at 93, the CAMBI gate at a mean of 1) and gated on the v1 original score at 93. `hevc_nvenc` ran with its adaptive quantization off, as sptenc runs it, and on, as in the other sections:

| Encoder, gate | Search | Video stream | Attempts per segment | Mean QP | CPU busy |
|---|---|---|---|---|---|
| `hevc_nvenc`, default | 19m10s | 176.5 MiB | 3.92 | 24.4 | 85% |
| `libx265`, default | 1h2m6s | 170.5 MiB | 3.88 | 25.9 | 88% |
| `hevc_nvenc` with AQ on, default | 18m20s, 18m51s | 200.9 MiB | 3.72 | 25.8 | 83% |
| `hevc_nvenc` with AQ on, v1 original | 17m46s | 283.4 MiB | 3.99 | 23.5 | 81% |
| `libx265`, v1 original | 1h1m14s | 196.5 MiB | 3.96 | 24.7 | 90% |

At the default, the `libx265` stream is 3% smaller than the `hevc_nvenc` one, for a search 3.2 times as long: smaller on 115 segments out of 163, larger on 48 (from 42% smaller to 43% larger). It was 15% smaller than the `hevc_nvenc` stream with AQ on, and 31% gated on the original score: fidelity made the `hevc_nvenc` stream (AQ on) 29% smaller than the original score did, the `libx265` one 13% (it raised the QP of `libx265` on 120 segments out of 163 and lowered none). At the default, the CAMBI gate lowered no segment of either encoder; the banding `libx265` added is 0.0045 on average over the frames and 3.64 on its worst frame (`hevc_nvenc`: 0.0038 and 3.04, and 0.0017 and 2.70 with AQ on).

## NVENC adaptive quantization

Under constant QP, NVENC honors its adaptive quantization: the QP asked for becomes a base, lowered where NVIDIA's model of the eye wants more bits. The 20 segments sampled for [the banding survey](#sources-against-themselves), encoded with sptenc's arguments and both AQ flags on or off, each encode scored for fidelity and the added banding. The size reaching fidelity 93 is interpolated on a log scale between the highest QP passing and the next one: `hevc_nvenc` encoded at every QP within 6 of that boundary, `av1_nvenc` bisected over its 0–255 range.

| AQ off against on, size reaching fidelity 93 | `hevc_nvenc` | `av1_nvenc` |
|---|---|---|
| All 20 segments | −22% | −22% |
| Film (10 segments) | −25% | −29% |
| Episode (10 segments) | −14% | −7% |
| Per segment | −4% to −37% | −4% to −49% |

Each of the 20 segments was smaller with AQ off, with both encoders. With `hevc_nvenc`, AQ on made 1.57 times the data at the same QP (median of 260 encodes, 1.07 to 3.13 times) for one more point of fidelity (median, −0.13 to +3.21): AQ off needed 0 to 3 QPs lower to reach 93 and still made smaller files. The CAMBI gate lowered no segment in either setting, and on the grain over black of the episode (segment 140), AQ off added less banding, 0.005 on average against 0.048. At normal levels the encodes of the darkest segments look the same; with their levels stretched, AQ on turns that grain into more flat blocks than AQ off. On the synthetic gradients, AQ off adds more banding at the same QP, and the CAMBI gate walks them further down, to about the size AQ on reached (see [The CAMBI gate on smooth gradients](#the-cambi-gate-on-smooth-gradients)).

### The default on the two contents

The whole contents at the default, `hevc_nvenc -C 6` (the rows "AQ off: the default" of [Gates on the two contents](#gates-on-the-two-contents)), against the same runs with AQ on. AQ off made the video stream 12% smaller on the episode and 30% on the film, not on every segment: 9 of the episode's 163 segments came out larger (up to 17%), and 2 of the film's 225, its end credits among them (24% larger at a QP one higher). It lowered the QP of 125 segments of the episode and raised it on 8 (from 4 lower to 3 higher), lowered it on 215 of the film and raised it on 2 (from 5 lower to 1 higher), for 5% and 7% more attempts, and searches 4.5% and 2.3% longer than the night runs with AQ on.

It added more banding. The CAMBI gate lowered no segment in either setting:

| Per segment, both contents (388) | AQ off | AQ on |
|---|---|---|
| Added banding, mean above 0.1 | 9 | 0 |
| Highest mean | 0.80 | 0.07 |
| Worst frame above 1, 3 and 5 | 31, 5 and 1 | 10, 0 and 0 |
| Highest worst frame | 6.30 | 2.70 |

The segment at 0.80 is grain on a dark wall behind a face, in the film (QP 20; 0.017 at QP 22 with AQ on). Both settings remove the grain, AQ off leaves flatter patches: 75 frames above 1, in bursts of up to 8. With the levels stretched 16 times the difference is plain; at normal levels, on its worst frame (3.62), the encodes look alike. One QP lower it adds 0.52, two lower 0.24 (1.7 times the size), three lower 0.04. The 6.30 is a single frame at the end of a fade to black in the film's end credits: the source is nearly black there, and the encode, at QP 37, still carries the blocks of the frame before, a few codes above black.

Over the whole files, the added banding is 0.0038 on average over the frames and 3.04 on the worst frame of the episode (0.0017 and 2.70 with AQ on), 0.0138 and 6.30 on the film (0.0014 and 1.79). v0 scores the outputs 94.32 and 95.14 (94.05 and 94.96 with AQ on), and 93 or more the segments holding 92.5% of the episode's frames and 94.7% of the film's: below it, the end credits of both, the studio logo of the film, and 5 segments of the episode within 0.8 of it.

## VideoToolbox

`hevc_videotoolbox` on an Apple M4 Max (14 cores: 10 performance, 4 efficiency), macOS 27.0, ffmpeg 9.0.1, encoding the anime episode of [Setup](#setup) at the default, 2 segments at a time (`-C 2`).

### A constant QP in uneven steps

The quality setting sptenc drives (`-q:v`) is a constant QP for HEVC. In the output of the episode (keyframes at most every 250 frames, B-frames), every frame of a segment carries the same HEVC QP, I, P and B frames alike, none of the 301 parameter sets allows a change per block, and each value gave the same QP on every segment. The values the search kept:

| `-q:v` | 66 | 64 | 62 | 61 | 60 | 57 | 55 | 54 | 53 | 51 | 46 | 43 | 42 | 41 | 38 |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| HEVC QP | 19 | 20 | 21 | 23 | 23 | 24 | 25 | 26 | 26 | 28 | 30 | 31 | 32 | 32 | 34 |

Neighbouring values often make the very same encode. On a segment of the episode (segment 107, 270 frames), `-q:v` 55 and 56, 57 to 59, 60 and 61, 62 and 63, 64 and 65, 66 and 67, 68 and 69 gave encodes of the same size to the byte and the same scores, in each of the settings where they were tried (keyframes every 12 frames, every 30 or at most every 250, with or without B-frames). No whole value gives QP 22: from `-q:v` 61 to 62, the fidelity of that segment jumped by about two points (91.87 to 93.98 with a keyframe every 12 frames). The search keeps the highest step that passes, and coarse steps land further above the gate: over the whole episode, a fidelity harmonic mean of 93.64 (93.68 without B-frames), against 93.43 with `hevc_nvenc` and 93.45 with `libx265`.

### Keyframe interval and B-frames

ffmpeg gives every encoder a keyframe every 12 frames and no B-frames, unless its wrapper replaces those defaults, and the VideoToolbox wrapper replaces neither (FFmpeg n9.0.2): sptenc passes `-g 250` and `-bf 1`. Segment 107 of the episode (270 frames), encoded alone, at the setting the search would keep, the highest `-q:v` step passing a fidelity harmonic mean of 93:

| Setting | Keyframes | Kept `-q:v` | Fidelity | Size |
|---|---|---|---|---|
| `-g 12`, ffmpeg's default | 23, every 12 frames | 62 | 93.98 | 4.07 MB |
| `-g 0`, left to VideoToolbox | 9, every 30 frames | 62 | 94.20 | 2.34 MB |
| `-g 250` | 2, frames 0 and 250 | 60 | 93.09 | 0.91 MB |
| `-g 250`, B-frames | 2 | 62 | 94.60 | 0.99 MB |

`libx265` and `hevc_nvenc`, left to their defaults, put their keyframes on the same frames of this segment as `-g 250`. Fewer keyframes raised the fidelity at the same `-q:v` (93.98, 94.20 and 94.89 at 62), which is how `-g 250` passed one step lower. `-bf 1` and `-bf 4` gave the same encode, same size and same pictures: the wrapper reads the value as allowed or not. At the same `-q:v`, B-frames made this segment 16 to 29% smaller for 0.2 to 0.6 less fidelity: 17% smaller at a fidelity of 93, interpolated on a log scale between the steps around it, but the coarse steps put its encodes at 92.66 and 94.60, on either side of the gate, and the encode kept is 9% larger than without B-frames.

The whole episode, each run from a cold cache:

| Settings | Video stream | Keyframe every | Attempts per segment | Search | CPU busy | Fidelity |
|---|---|---|---|---|---|---|
| ffmpeg's defaults (12 frames, no B-frames) | 322.4 MiB | 11.6 frames | 4.73 | 51m25s | 99% | 93.64 |
| `-g 250` | 267.8 MiB | 114.0 frames | 4.80 | 43m38s | 98% | 93.68 |
| `-g 250`, B-frames: sptenc's | 221.2 MiB | 124.6 frames | 4.79 | 43m39s | 98% | 93.64 |

`-g 250` made the video 17% smaller than ffmpeg's defaults, smaller on 147 of the 159 segments cut the same way (the first run was cut by an older build), 3 to 7 times on the end credits and segment 107. 122 of those segments kept their QP, 32 needed a lower one, 5 got a higher one. B-frames made it 17% smaller again than `-g 250` alone, smaller on 155 of the 163 segments, with 74% of the frames B-frames: the kept QP moved on 19 segments only, 10 up and 9 down, the coarse steps landing on either side of the gate about as often. The steps of the first run before the search took longer too (2m51s to count the frames, against 2m18s): its longer search can not be attributed to the settings alone. With B-frames, the timestamps of the output are those of the run without them, and of the `hevc_nvenc` output, on every frame.

## Banding

CAMBI rates banding from 0 (none) up, and "a CAMBI score around 5 is where banding starts to become slightly annoying" ([CAMBI documentation](https://github.com/Netflix/vmaf/blob/master/resource/doc/cambi.md)). The v1 models cap it at 17 in their score; the banding sptenc measures is not capped. Unless said otherwise, it is computed with the settings of the v1 models (`cambi_high_res_speedup=1080`, `cambi_vis_lum_threshold=0.06`).

### Sources against themselves

Every segment of both contents scored against itself with the v1 original score: median 98.8 on the film, 96.9 on the episode. 16% and 12% of the frames are in segments at 99.9 or above, 35% and 55% in segments below 97, down to 90.9. CAMBI rates the source's own banding as if the encoder had made it. Fidelity scored 100 on the 8 segments measured, 4 banded and 4 clean.

That banding bends the curve of the original score. A 9 s dark scene of the film (source CAMBI 14.0) scores 91.8 against itself, its encodes less banded by CAMBI's measure (9.5 at QP 0, 7.1 at QP 9, 3.5 at QP 20) and scoring above it: a peak of 94.05 at QP 4, 93.65 at QP 0. The search at 93 kept QP 9, where v0 had been satisfied at QP 20 with a stream 15 times smaller.

Sampled every 2 QPs from 0 to 40 on 20 segments of both contents (self-scores from 93.5 to 100), fidelity never rose with the QP and is 99.94 to 100 at QP 0. The original score rose on 15 of them, by up to 0.41.

### Added banding at the QPs the VMAF search picked

The 776 segment encodes of fidelity at 93 and 95 on both contents, with AQ on (the default with AQ off adds more: see [The default on the two contents](#the-default-on-the-two-contents)), the statistic of each segment over its frames:

| Per segment | Above 0.5 | Above 1 | Above 2 | Above 3 | Above 5 | Highest |
|---|---|---|---|---|---|---|
| Mean | 0 | 0 | 0 | 0 | 0 | 0.10 |
| 95th percentile | 1 | 0 | 0 | 0 | 0 | 0.52 |
| 99th percentile | 7 | 2 | 1 | 0 | 0 | 2.35 |
| Worst frame | 39 | 13 | 5 | 2 | 1 | 10.68 |

The frames above 1 come in bursts of at most 8 frames. The 10.68 is three frames of a studio logo on flat blue, whose fine line texture the encoder turned into flat plateaus one code apart (visible with the contrast raised 48 times). One QP lower, at 29, the same frames add 0.6 at most, and 0.11 at most at QP 33. The 3.42 of the episode is grain on a near-black background turned into flat one-code blocks.

Over the 1,403 attempts of the two fidelity searches at 93 that measured it on every attempt, at every QP they tried, a single segment mean is above 0.5: 0.51, at a QP fidelity rejected (91.88), on the only segment whose added banding climbs past 0.1 as the QP rises (0.017 at QP 22, 0.218 at 23, 0.514 at 24).

The added banding is not monotonic in QP. On the 20 segments sampled from QP 0 to 40, 18 add nothing above 0.5 on any frame. One adds more from QP 30 up, above the QP fidelity picks for it at 93 (21). The worst frame of the episode's segment above adds 0.78 at QP 16, 4.89 at 20 to 22, 1.52 at 28, 0.17 at 30 and 2.37 at 40, fidelity picking QP 27 at 93. At high QP, the coarse steps escape CAMBI and fidelity is what catches them.

### The worst frame threshold

Its cost on the episode's segment above: at fidelity 95 (QP 25, worst frame 3.42), any worst frame threshold from 0.78 to 3 takes it down to QP 16 or 17 (its worst frame is 3.3 to 4.9 from QP 18 to 24, and 0.78 at 16, sampled every 2 QPs), four times the size of that segment, and a lower threshold to QP 14 or 15. At fidelity 93 (QP 27, 2.23), a threshold of 3 passes and one of 2 takes it down to the same QPs. With a threshold of 3, the logo segment at 95 needs one QP less (29: 2.09).

### The CAMBI gate on smooth gradients

Four synthetic 10-bit 1080p clips of 5 s where encoders add banding, encoded with sptenc's `hevc_nvenc` and `libx265` arguments: `hevc_nvenc` at every QP from 0 to 50, `libx265` and `hevc_nvenc` with AQ on every 2 QPs from 10 to 50 (and 0 to 8 where needed). The QP fidelity at 93 picks, the mean banding the encoder added there, and where a mean of 1 takes it; run on the four clips with `hevc_nvenc`, sptenc itself kept the QPs of the rows without AQ:

| Clip | Encoder | Fidelity at 93 (added mean) | With the CAMBI gate | File size |
|---|---|---|---|---|
| Dark ramp brightening over time | `hevc_nvenc` | QP 38 (3.17) | QP 19 (0.39) | 0.80 times |
| Dark ramp brightening over time | `hevc_nvenc` with AQ on | QP 46 (2.05) | QP 28 (0.36) | 1.09 times |
| Bluish vertical gradient (sky) | `libx265` | QP 36 (3.28) | QP 34 (0) | 1.02 times |
| The sky with grain | `hevc_nvenc` | QP 12 (2.76) | QP 3 (0.92) | 199 times |
| The sky with grain | `hevc_nvenc` with AQ on | QP 20 (3.02) | QP 10 (0.88) | 174 times |
| The sky with grain | `libx265` | QP 14 (2.07) | QP 4 (0) | 117 times |
| Radial vignette | `hevc_nvenc` | QP 35 (0.60) | QP 35 | the same |
| Radial vignette | `hevc_nvenc` with AQ on | QP 42 (0.78) | QP 42 | the same |
| The ramp, the sky, the vignette | the other encoder | 0 to 0.16 | the same | the same |

Sampled every 2 QPs, the gate, walking one QP at a time, can stop one QP higher. Fidelity rises with the QP on 7 of the 8 pairs sampled every 2 QPs, by up to 2.35, and over the same QPs on 3 of the 4 clips with `hevc_nvenc` AQ off, by up to 1.15: these clips cost the encoders a few hundred bytes per frame, and the ramp encode the gate keeps is even smaller than fidelity's. The added banding does not always shrink as the QP goes down either: at high QP the steps turn coarse and CAMBI rates them lower (the worst frame of the `libx265` ramp adds 9.67 at QP 36, nothing at 50). The vignette gets more than 0.5 from `hevc_nvenc` at every QP from 0 to 40, 0.74 to 1.19 below the QP fidelity picks: a gate at 0.5 would walk it down to QP 0 for nothing and keep QP 35, a CAMBI best effort. With AQ on, it got 0.63 to 1.28 from QP 6 to 42 and 0.22 at QP 0, and a gate at 0.5 would have taken it from QP 42 down to 4.

The clips, as ffmpeg `lavfi` graphs (`nullsrc=s=1920x1080:r=24000/1001:d=5,format=yuv420p10le,` then):
- ramp: `geq=lum='64+160*X/W+20*T':cb=512:cr=512`
- sky: `geq=lum='300+400*Y/H':cb=600:cr=450`
- sky with grain: the sky, then `noise=alls=1:allf=t`
- vignette: `geq=lum='120+400*(1-hypot(X-W/2\,Y-H/2)/hypot(W/2\,H/2))':cb=512:cr=512`

### The models' settings against full resolution

libvmaf applies `cambi_high_res_speedup` from 1920×1080 pixels up (a 1920×800 source is computed at full resolution either way), and its documentation expects "some loss of accuracy". On the 776 encodes above, the segment means of both settings are within 0.085 of each other and never on opposite sides of 0.5, 1, 2 or 3. On the worst frame, the models' setting reported more banding by over 0.05 on 68 encodes, full resolution on 3 (10.68 against 11.76 on the logo). They disagree on the source: full resolution rates grain on a near-black background as banding (5.25 against 2.78 on one frame, the encode 4.04 and 4.03), and then sees no banding added where the encoder turned that grain into flat blocks, which the models' setting flags (1.26).

## Synthetic clips

What the [What VMAF sees](MANUAL.md#what-vmaf-sees-and-what-it-does-not) section of the manual quotes: 10 to 12 frames of 10-bit 1080p each, scored with libvmaf against their reference, CAMBI with the settings above.

| Clip | Reference | v0.6.1 | v0.6.1 NEG | v1 original | Fidelity | CAMBI added |
|---|---|---|---|---|---|---|
| `testsrc2` | itself | 99.65 | 99.65 | 100 | 100 | 0 |
| Hues rotated by 90° | `testsrc2` | 99.65 | 99.65 | 65.93 | 65.97 | 0 |
| Chroma blurred (radius 10) | `testsrc2` | 99.65 | 99.65 | 66.18 | 66.22 | 0 |
| Grayscale | `testsrc2` | 99.65 | 99.65 | 100 | 100 | 0 |
| Dark gradient | itself | 97.40 | 97.40 | 95.89 | 100 | 0 |
| Posterized to 18 levels | dark gradient | 100 | 96.19 | 91.24 | 91.24 | 0 |
| Posterized to 10 levels | dark gradient | 100 | 95.96 | 87.90 | 87.90 | 0 |
| Posterized to 5 levels | dark gradient | 100 | 95.96 | 82.12 | 82.12 | 0 |
| 8-bit-like steps | dark gradient | 99.26 | 96.28 | 84.52 | 95.19 | 15.83 |

CAMBI rates the dark gradient 6.65 and its 8-bit-like steps 22.48: 15.83 added. It rates the posterized gradients 0, which is why both v1 scores agree on them. The clips, at 24 fps, are converted to 10 bits after their source (`ffmpeg -f lavfi -i <source> -vf format=yuv420p10le,<filter>`: `testsrc2` drawn in 10 bits directly moves the second decimals):
- `testsrc2=s=1920x1080`; hues rotated: `geq=lum='lum(X,Y)':cb='1024-cr(X,Y)':cr='cb(X,Y)'`; chroma blurred: `boxblur=luma_radius=0:chroma_radius=10`; grayscale: `lutyuv=u=512:v=512`
- dark gradient, one 10-bit code every 12 pixels from 64 to 223: `geq=lum='64+floor(X/12)':cb=512:cr=512` on a 1920×1080 `color=black`
- posterized to N levels: `lum='64+floor(floor((X/12)/S)*S)'` with S = 160/N
- 8-bit-like steps, 4 codes every 48 pixels, the same slope: `lum='64+4*floor(X/48)'`

## Minimum picture sizes

Below a minimum size, libvmaf can not measure a picture, and it does not always say so. Tested with libvmaf 3.2.1 the way sptenc's probe does: two frames of `testsrc2` at that size, in 10 bits, scored against themselves; a size passes when ffmpeg exits successfully with a report holding every metric the pass asks for. The sizes on each side of the limits found:

| Pass | Passes | Fails |
|---|---|---|
| `vmaf_v1.0.16_3d0h` and `vmaf_v1.0.16_1d5h_2160`, the models sptenc selects | 216×160, 1864×160 | 216×159 (crash), 215×160 (no report, ffmpeg exits successfully), 1872×160 and 1920×160 (crash once the report is written) |
| `vmaf_v1.0.16_5d0h`, phone | 472×266, 480×270 | 472×265, 470×264 (crash) |
| `vmaf_v1.0.16_3d0h_2160`, 4K at 3 times the height | 566×318, 568×320 | 564×317 (crash) |
| The banding measure, whatever the model | 216×160, 160×216 | 215×160, 215×215 (a report without frames, ffmpeg exits successfully) |

The banding measure needs 216 pixels on one side at least, as libvmaf's CAMBI requires (`libvmaf/src/feature/cambi.c`). The v1 models feed on CAMBI, hence their 216 pixels too: a v0 model, without CAMBI, scores 215×160.
