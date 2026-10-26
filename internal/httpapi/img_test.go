package httpapi

import (
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
		if got := refererFor(u); got != want {
			t.Errorf("refererFor(%q) = %q, 期望 %q", in, got, want)
		}
	}
}
