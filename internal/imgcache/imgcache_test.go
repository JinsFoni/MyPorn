package imgcache

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestWithImageWidthRewritesBeegPhotos(t *testing.T) {
	cases := []struct{ in, width, want string }{
		{
			"https://thumbs.externulls.com/photos/809867252018185/to.webp?crop_id=3008", "240",
			"https://thumbs.externulls.com/photos/809867252018185/to.webp?crop_id=3008&size_new=240x240",
		},
		{
			"https://thumbs.externulls.com/photos/809867252018185/to.webp?crop_id=3008", "",
			"https://thumbs.externulls.com/photos/809867252018185/to.webp?crop_id=3008",
		},
		{
			// 已经带了尺寸参数就不重复加
			"https://thumbs.externulls.com/photos/1/to.webp?size_new=320x320", "240",
			"https://thumbs.externulls.com/photos/1/to.webp?size_new=320x320",
		},
		{
			// 视频预览图（/videos/）不做改写
			"https://thumbs.externulls.com/videos/490004545724226/0.webp?size=480x270", "240",
			"https://thumbs.externulls.com/videos/490004545724226/0.webp?size=480x270",
		},
		{
			// 其他图床原样返回
			"https://ci.phncdn.com/videos/202401/01/1.jpg", "240",
			"https://ci.phncdn.com/videos/202401/01/1.jpg",
		},
		{
			// 非法宽度忽略
			"https://thumbs.externulls.com/photos/1/to.webp", "abc",
			"https://thumbs.externulls.com/photos/1/to.webp",
		},
	}
	for _, c := range cases {
		u, err := url.Parse(c.in)
		if err != nil {
			t.Fatal(err)
		}
		if got := withImageWidth(u, c.width).String(); got != c.want {
			t.Errorf("withImageWidth(%q, %q) = %q, 期望 %q", c.in, c.width, got, c.want)
		}
	}
}

func TestRefererFor(t *testing.T) {
	cases := map[string]string{
		// beeg 图床拒绝自家根域的 Referer，必须给 beeg.com
		"https://thumbs.externulls.com/photos/1/to.webp": "https://beeg.com/",
		// 其他 CDN 仍用根域近似
		"https://ci.phncdn.com/videos/1.jpg": "https://phncdn.com/",
	}
	for in, want := range cases {
		u, err := url.Parse(in)
		if err != nil {
			t.Fatal(err)
		}
		if got := RefererFor(u); got != want {
			t.Errorf("RefererFor(%q) = %q, 期望 %q", in, got, want)
		}
	}
}

// 一个真图片服务：/img.png 正常给图，/broken 给 HTML。
func imageServer(t *testing.T) (*httptest.Server, *int) {
	t.Helper()
	hits := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/img.png", func(w http.ResponseWriter, r *http.Request) {
		hits++
		img := image.NewRGBA(image.Rect(0, 0, 320, 180))
		for y := 0; y < 180; y++ {
			for x := 0; x < 320; x++ {
				img.Set(x, y, color.RGBA{R: uint8(x % 255), G: uint8(y % 255), B: 120, A: 255})
			}
		}
		w.Header().Set("Content-Type", "image/png")
		_ = png.Encode(w, img)
	})
	mux.HandleFunc("/broken", func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<html>403</html>"))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, &hits
}

func testService(t *testing.T) *Service {
	t.Helper()
	return New(t.TempDir(), nil, "", slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func TestBytesFetchesThenCaches(t *testing.T) {
	srv, hits := imageServer(t)
	svc := testService(t)
	url := srv.URL + "/img.png"

	data, ok := svc.Bytes(context.Background(), "pornhub", url, "")
	if !ok || len(data) == 0 {
		t.Fatal("第一次应当抓下来")
	}
	if !svc.Cached("pornhub", url) {
		t.Error("抓完应当已经在缓存里")
	}
	if _, ok := svc.Bytes(context.Background(), "pornhub", url, ""); !ok {
		t.Fatal("第二次应当命中缓存")
	}
	if *hits != 1 {
		t.Errorf("上游被请求 %d 次，期望 1 次", *hits)
	}
}

func TestSmallReusesCachedOriginal(t *testing.T) {
	srv, hits := imageServer(t)
	svc := testService(t)
	url := srv.URL + "/img.png"
	if _, ok := svc.Bytes(context.Background(), "pornhub", url, ""); !ok {
		t.Fatal("原图应当抓下来")
	}
	small, err := svc.Small(context.Background(), "pornhub", url, 48)
	if err != nil {
		t.Fatalf("Small: %v", err)
	}
	img, err := jpeg.Decode(bytes.NewReader(small))
	if err != nil {
		t.Fatalf("小图应当是合法 JPEG：%v", err)
	}
	if got := img.Bounds().Dx(); got != 48 {
		t.Errorf("小图宽 = %d，期望 48", got)
	}
	if *hits != 1 {
		t.Errorf("缩图应当复用已缓存的原图，上游却被请求了 %d 次", *hits)
	}
	// 再来一次应当直接命中缓存的小图
	if _, err := svc.Small(context.Background(), "pornhub", url, 48); err != nil {
		t.Fatal(err)
	}
	if *hits != 1 {
		t.Errorf("小图也应当缓存，上游被请求了 %d 次", *hits)
	}
}

func TestSmallFailsOnNonImage(t *testing.T) {
	srv, _ := imageServer(t)
	svc := testService(t)
	if _, err := svc.Small(context.Background(), "pornhub", srv.URL+"/broken", 48); err == nil {
		t.Fatal("抓回来的不是图片就该报错（上层据此发空白占位）")
	}
}

func TestBytesRejectsBadURL(t *testing.T) {
	svc := testService(t)
	for _, raw := range []string{"", "  ", "file:///etc/passwd", "not-a-url", "ftp://x/y.png"} {
		if _, ok := svc.Bytes(context.Background(), "pornhub", raw, ""); ok {
			t.Errorf("Bytes(%q) 不该成功", raw)
		}
	}
}
