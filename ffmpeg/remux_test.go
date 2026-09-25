package ffmpeg

import "testing"

// The range declared for the output must be the new video's whenever it declares one, the
// original's being only a fallback (see RemuxColorRange).
func TestRemuxColorRange(t *testing.T) {
	stream := func(colorRange string) *FFProbeBinaryStream {
		return &FFProbeBinaryStream{CodecType: "video", ColorRange: colorRange}
	}
	tests := []struct {
		name          string
		originalVideo *FFProbeBinaryStream
		newVideo      *FFProbeBinaryStream
		want          string
	}{
		// the master converts full range sources to limited range
		{"full range original, limited range new video", stream("pc"), stream("tv"), "tv"},
		{"limited range original and new video", stream("tv"), stream("tv"), "tv"},
		// the remux command is given any new video
		{"limited range original, full range new video", stream("tv"), stream("pc"), "pc"},
		{"original declaring no range", stream(""), stream("tv"), "tv"},
		{"original not probed", nil, stream("tv"), "tv"},
		// fallbacks on the original's range
		{"new video declaring no range", stream("pc"), stream(""), "pc"},
		{"new video declaring an unknown range", stream("pc"), stream("unknown"), "pc"},
		{"new video not probed", stream("pc"), nil, "pc"},
		{"no range anywhere", stream(""), stream(""), ""},
		{"nothing probed", nil, nil, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := RemuxColorRange(tt.originalVideo, tt.newVideo); got != tt.want {
				t.Errorf("RemuxColorRange() = %q, want %q", got, tt.want)
			}
		})
	}
}
