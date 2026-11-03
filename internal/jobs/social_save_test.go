package jobs

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/JinsFoni/MyPorn/internal/events"
	"github.com/JinsFoni/MyPorn/internal/model"
	"github.com/JinsFoni/MyPorn/internal/store"
)

// 目录/文件名规则（2026-11-04 用户要求「按名字/年/月分目录」）：
// {图片库根}/{博主名或@handle}/{年}/{月}/{日期}_{帖子 id 末 8 位}_{序号}.jpg
func TestPhotoRelDirAndBaseName(t *testing.T) {
	posted := time.Date(2026, 3, 9, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name     string
		photo    *store.SocialPhotoRow
		wantDir  string
		wantBase string
	}{
		{
			name: "有显示名有日期",
			photo: &store.SocialPhotoRow{
				Seq: 1, PostedAt: &posted,
				Account: store.SocialAccountBrief{Handle: "kiho_kanematsu", DisplayName: "金松季歩(kanematsu kiho)"},
				Post:    store.SocialPostBrief{URL: "https://x.com/k/status/2115129428158087498"},
			},
			wantDir:  "金松季歩(kanematsu kiho)/2026/03",
			wantBase: "2026-03-09_58087498_01",
		},
		{
			name: "没有显示名用 @handle，序号补两位",
			photo: &store.SocialPhotoRow{
				Seq: 12, PostedAt: &posted,
				Account: store.SocialAccountBrief{Handle: "mysaaat"},
				Post:    store.SocialPostBrief{URL: "https://x.com/m/status/9"},
			},
			wantDir:  "mysaaat/2026/03",
			wantBase: "2026-03-09_9_12",
		},
		{
			name: "时间缺失的老帖落「未标注」，不塞进本月",
			photo: &store.SocialPhotoRow{
				Seq: 0,
				Account: store.SocialAccountBrief{Handle: "jun"},
				Post:    store.SocialPostBrief{URL: "https://instagram.com/p/Cx1y2z3AbCd/"},
			},
			wantDir:  "jun/未标注/未标注",
			wantBase: "y2z3AbCd_01", // 只取末 8 位
		},
	}
	for _, c := range cases {
		if got := photoRelDir(c.photo); got != c.wantDir {
			t.Errorf("%s：目录 = %q，期望 %q", c.name, got, c.wantDir)
		}
		if got := photoBaseName(c.photo); got != c.wantBase {
			t.Errorf("%s：文件名 = %q，期望 %q", c.name, got, c.wantBase)
		}
	}
}

// 落盘全流程：设置里的根目录 → {名字}/{年}/{月}/ → 写文件 + 记账 + post.json。
func TestSocialSaveJobWritesPhotoRootLayout(t *testing.T) {
	r, st, _ := newSocialFixture(t, "jun_kasui")
	root := filepath.Join(t.TempDir(), "图片库")
	if err := st.SetSetting("photo_root", root); err != nil {
		t.Fatalf("写设置失败: %v", err)
	}
	acc, err := st.GetSocialAccountByHandle("x", "jun_kasui")
	if err != nil {
		t.Fatalf("取博主失败: %v", err)
	}
	// displayName 才是目录名（用户要的是「名字」，不是 @handle）
	if _, err := st.UpsertSocialAccount(&store.SocialAccount{
		Platform: "x", Handle: acc.Handle, DisplayName: "香水じゅん",
	}); err != nil {
		t.Fatalf("改进显示名失败: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write([]byte("jpeg-bytes-0123456789"))
	}))
	defer srv.Close()

	posted := time.Date(2026, 3, 9, 12, 0, 0, 0, time.UTC)
	ids, err := st.InsertSocialPosts(acc.ID, "x", []store.SocialPostInput{{
		PostID:   "2115129428158087498",
		URL:      "https://x.com/jun/status/2115129428158087498",
		Caption:  "今天也在",
		PostedAt: posted,
		Photos: []store.SocialPhotoInput{
			{URL: srv.URL + "/a.jpg", Width: 10, Height: 10, Seq: 1},
			{URL: srv.URL + "/b.jpg", Width: 10, Height: 10, Seq: 2},
		},
	}})
	if err != nil || len(ids) != 2 {
		t.Fatalf("写帖子失败: %v ids=%v", err, ids)
	}

	runSocialSave(t, r, st, ids, acc.ID)

	dir := filepath.Join(root, "香水じゅん", "2026", "03")
	for _, name := range []string{
		"2026-03-09_58087498_01.jpg",
		"2026-03-09_58087498_02.jpg",
		"2026-03-09_58087498.post.json",
	} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("缺文件 %s: %v", name, err)
		}
	}

	// 落盘记账挂在「图片库」（library_id = 0），路径相对图片库根。
	saved, err := st.PhotoSaved(ids[0])
	if err != nil || !saved {
		t.Fatalf("没记上落盘记录: %v %v", saved, err)
	}
	var relPath string
	var libraryID int64
	if err := st.DB().QueryRow(`SELECT rel_path, library_id FROM social_photo_files WHERE photo_id = ?`, ids[0]).
		Scan(&relPath, &libraryID); err != nil {
		t.Fatalf("读落盘记录失败: %v", err)
	}
	want := "香水じゅん/2026/03/2026-03-09_58087498_01.jpg"
	if relPath != want {
		t.Fatalf("rel_path = %q，期望 %q", relPath, want)
	}
	if libraryID != photoRootLibraryID {
		t.Fatalf("library_id = %d，期望 %d（图片库）", libraryID, photoRootLibraryID)
	}

	// 帖子元数据里要有这帖已落盘的两张（清单来自库，不靠扫目录）。
	meta, err := os.ReadFile(filepath.Join(dir, "2026-03-09_58087498.post.json"))
	if err != nil {
		t.Fatalf("读 post.json 失败: %v", err)
	}
	var payload struct {
		Caption string   `json:"caption"`
		Photos  []string `json:"photos"`
	}
	if err := json.Unmarshal(meta, &payload); err != nil {
		t.Fatalf("解析 post.json 失败: %v", err)
	}
	if payload.Caption != "今天也在" || len(payload.Photos) != 2 {
		t.Fatalf("post.json 内容不对: %+v", payload)
	}

	// 再跑一次：都已落盘，队列为空，不该重新下载（也不会因「目录里已有同名文件」而改名）。
	runSocialSave(t, r, st, ids, acc.ID)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("重跑后目录里应仍只有 3 个文件，实际 %v", names)
	}
}

func runSocialSave(t *testing.T, r *Runner, st *store.Store, ids []int64, accountID int64) {
	t.Helper()
	payload, _ := json.Marshal(socialSavePayload{PhotoIDs: ids, AccountID: accountID})
	job := &model.Job{ID: 2, Type: model.JobTypeSocialSave, Payload: string(payload)}
	ctx := t.Context()
	ctl := &Ctl{ctx: ctx, cancel: func(error) {}, st: st, hub: events.NewHub(),
		log: slog.New(slog.NewTextHandler(io.Discard, nil)), job: job}
	if err := r.runSocialSaveJob(ctx, job, ctl); err != nil {
		t.Fatalf("存图任务不该失败: %v", err)
	}
}

func TestPostIDFromURL(t *testing.T) {
	cases := map[string]string{
		"https://x.com/NASA/status/2115129428158087498": "2115129428158087498",
		"https://twitter.com/nasa/status/123?s=20":      "123",
		"https://www.instagram.com/p/Cx1y2z3AbCd/":      "Cx1y2z3AbCd",
		"https://www.instagram.com/nasa/p/Cx1y2z3AbCd/": "Cx1y2z3AbCd",
		"https://x.com/nasa":                            "nasa",
		"":                                              "",
	}
	for in, want := range cases {
		if got := postIDFromURL(in); got != want {
			t.Errorf("postIDFromURL(%q) = %q，期望 %q", in, got, want)
		}
	}
}

func TestTail(t *testing.T) {
	cases := []struct {
		in   string
		n    int
		want string
	}{
		{"2115129428158087498", 8, "58087498"},
		{"abc", 8, "abc"},
		{"", 4, ""},
		{"老帖id", 2, "id"}, // 按 rune 数，不是字节数
	}
	for _, c := range cases {
		if got := tail(c.in, c.n); got != c.want {
			t.Errorf("tail(%q, %d) = %q，期望 %q", c.in, c.n, got, c.want)
		}
	}
}
