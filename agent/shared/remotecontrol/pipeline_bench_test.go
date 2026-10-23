package remotecontrol

import (
	"math/rand"
	"testing"
)

// syntheticDesktop returns a BGRA frame that compresses roughly like a real
// desktop: flat panels, gradients and a band of high-detail "text". A frame of
// pure noise would make every encoder look equally slow, and a flat one would
// make them all look free.
func syntheticDesktop(w, h int, seed int64) []byte {
	r := rand.New(rand.NewSource(seed))
	px := make([]byte, w*h*4)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			i := (y*w + x) * 4
			switch {
			case y%40 < 14 && x%9 < 6 && (x/9+y/40)%3 != 0:
				v := byte(r.Intn(60))
				px[i], px[i+1], px[i+2] = v, v, v
			case y < h/3:
				px[i], px[i+1], px[i+2] = byte(x*255/w), byte(y*255/h), 180
			default:
				px[i], px[i+1], px[i+2] = 235, 235, 238
			}
			px[i+3] = 0xFF
		}
	}
	return px
}

// BenchmarkEncodeFullFrame1080p is the cost of the worst common case: scrolling,
// a window drag or video, where the whole desktop is damaged every frame.
func BenchmarkEncodeFullFrame1080p(b *testing.B) {
	const w, h = 1920, 1080
	px := syntheticDesktop(w, h, 1)
	rects := []Rect{{W: w, H: h}}
	b.SetBytes(int64(w * h * 4))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := EncodeFrameQuality(px, w, h, rects, defaultQuality); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkDiff1080p is the per-tick cost of finding what changed.
func BenchmarkDiff1080p(b *testing.B) {
	const w, h = 1920, 1080
	a := syntheticDesktop(w, h, 1)
	c := syntheticDesktop(w, h, 2)
	d := NewDamage()
	d.SetSize(w, h)
	d.Diff(a)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if i%2 == 0 {
			d.Diff(c)
		} else {
			d.Diff(a)
		}
	}
}
