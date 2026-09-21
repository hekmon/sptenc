package ffmpeg

import "testing"

func TestAdaptVMAFPath(t *testing.T) {
	for _, tc := range []struct {
		path, expected string
	}{
		{`/tmp/sptenc-123/seg_000000_qp026.mkv_vmaf.json`, `/tmp/sptenc-123/seg_000000_qp026.mkv_vmaf.json`},
		{`/tmp/with space/a=b/r.json`, `/tmp/with space/a=b/r.json`},
		{`/tmp/it's/r.json`, `/tmp/it\\\'s/r.json`},
		{`/tmp/a,b;c[d]/r.json`, `/tmp/a\,b\;c\[d\]/r.json`},
		{`/tmp/a:b/r.json`, `/tmp/a\\:b/r.json`},
		{`C:\Users\O'Brien\AppData\Local\Temp\r.json`, `C\\:\\\\Users\\\\O\\\'Brien\\\\AppData\\\\Local\\\\Temp\\\\r.json`},
	} {
		if got := adaptVMAFPath(tc.path); got != tc.expected {
			t.Errorf("path %q: expected %q, got %q", tc.path, tc.expected, got)
		}
	}
}
