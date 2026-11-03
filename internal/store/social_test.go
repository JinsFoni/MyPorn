package store

import (
	"testing"
	"time"

	"github.com/JinsFoni/MyPorn/internal/model"
)

func TestUpsertSocialAccount(t *testing.T) {
	st := newTestStore(t)
	acc, err := st.UpsertSocialAccount(&SocialAccount{
		Platform: "x", Handle: "Mini_Diva", DisplayName: "Mini Diva",
		AvatarURL: "https://pbs.twimg.com/a.jpg", ProfileURL: "https://x.com/mini_diva",
	})
	if err != nil {
		t.Fatal(err)
	}
	if acc.ID == 0 {
		t.Fatal("应该拿到 id")
	}
	if acc.Handle != "mini_diva" {
		t.Errorf("handle 应转小写，得到 %q", acc.Handle)
	}
	if !acc.Enabled {
		t.Error("新博主默认启用")
	}

	// 重复添加同一个账号：不新建行，且空字段不覆盖已有值。
	again, err := st.UpsertSocialAccount(&SocialAccount{
		Platform: "x", Handle: "mini_diva", DisplayName: "",
	})
	if err != nil {
		t.Fatal(err)
	}
	if again.ID != acc.ID {
		t.Errorf("同一账号应复用同一条记录：%d != %d", again.ID, acc.ID)
	}
	if again.DisplayName != "Mini Diva" {
		t.Errorf("空显示名不该覆盖已有值，得到 %q", again.DisplayName)
	}
	if again.AvatarURL == "" {
		t.Error("头像被清空了")
	}
}

func TestSocialPostsDedupe(t *testing.T) {
	st := newTestStore(t)
	acc, err := st.UpsertSocialAccount(&SocialAccount{Platform: "x", Handle: "mini"})
	if err != nil {
		t.Fatal(err)
	}
	posted := time.Date(2026, 10, 23, 23, 0, 0, 0, time.UTC)
	posts := []SocialPostInput{
		{
			PostID: "111", URL: "https://x.com/mini/status/111", Caption: "hi", PostedAt: posted,
			Photos: []SocialPhotoInput{
				{URL: "https://pbs.twimg.com/media/A.jpg", Width: 100, Height: 200, Seq: 1},
				{URL: "https://pbs.twimg.com/media/B.jpg", Width: 300, Height: 400, Seq: 2},
			},
		},
		// 没图的帖子也要落一条（增量同步靠它判断"这条看过"）。
		{PostID: "222", URL: "https://x.com/mini/status/222", PostedAt: posted.Add(-time.Hour)},
	}
	ids, err := st.InsertSocialPosts(acc.ID, "x", posts)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 {
		t.Fatalf("新照片数 = %d，期望 2", len(ids))
	}

	// 同一批再写一次：一条新行都不该产生。
	ids2, err := st.InsertSocialPosts(acc.ID, "x", posts)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids2) != 0 {
		t.Errorf("重复写入不该产生新照片，得到 %d 条", len(ids2))
	}

	exists, err := st.HasSocialPost("x", "222")
	if err != nil || !exists {
		t.Errorf("222 应已入库: %v %v", exists, err)
	}
	if exists, _ := st.HasSocialPost("x", "999"); exists {
		t.Error("999 不该存在")
	}

	if err := st.RefreshSocialCounts(acc.ID); err != nil {
		t.Fatal(err)
	}
	fresh, err := st.GetSocialAccount(acc.ID)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.PostCount != 2 || fresh.PhotoCount != 2 {
		t.Errorf("计数不对：%d 帖 / %d 张", fresh.PostCount, fresh.PhotoCount)
	}
}

// TestHideSocialPhotoStaysHidden 守住「删掉的图不会被下一次同步塞回来」。
//
// 这是软删的全部意义：`social_photos` 上有 UNIQUE (platform, url)，写入是 INSERT OR IGNORE。
// 如果实现改成真删行，第二轮同步会把这同一张图重新插进来，用户在墙上删掉的照片
// 六小时后又出现了。这里直接把「删 → 再同步一遍」跑出来看。
func TestHideSocialPhotoStaysHidden(t *testing.T) {
	st := newTestStore(t)
	acc, err := st.UpsertSocialAccount(&SocialAccount{Platform: "x", Handle: "mini"})
	if err != nil {
		t.Fatal(err)
	}
	posted := time.Date(2026, 11, 3, 8, 41, 0, 0, time.UTC)
	posts := []SocialPostInput{{
		PostID: "333", URL: "https://x.com/mini/status/333", PostedAt: posted,
		Photos: []SocialPhotoInput{
			{URL: "https://pbs.twimg.com/media/C.jpg", Seq: 1},
			{URL: "https://pbs.twimg.com/media/D.jpg", Seq: 2},
		},
	}}
	if _, err := st.InsertSocialPosts(acc.ID, "x", posts); err != nil {
		t.Fatal(err)
	}
	got, _, err := st.ListMoments(MomentsQuery{AccountID: acc.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("墙上应有 2 张，得到 %d", len(got))
	}
	target := got[0].ID

	accountID, err := st.HideSocialPhoto(target)
	if err != nil {
		t.Fatal(err)
	}
	if accountID != acc.ID {
		t.Errorf("返回的账号 id 不对：%d != %d", accountID, acc.ID)
	}

	// 再同步一遍（同样的 url）——不该把删掉的那张插回来。
	if _, err := st.InsertSocialPosts(acc.ID, "x", posts); err != nil {
		t.Fatal(err)
	}
	if err := st.RefreshSocialCounts(acc.ID); err != nil {
		t.Fatal(err)
	}
	got, _, err = st.ListMoments(MomentsQuery{AccountID: acc.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("删掉的那张被同步塞回来了：墙上 %d 张", len(got))
	}
	if got[0].ID == target {
		t.Error("被删的那张还在墙上")
	}
	fresh, err := st.GetSocialAccount(acc.ID)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.PhotoCount != 1 {
		t.Errorf("照片计数应降到 1，得到 %d", fresh.PhotoCount)
	}
	if fresh.PostCount != 1 {
		t.Errorf("帖子计数不该受影响，得到 %d", fresh.PostCount)
	}

	// 不存在的 id：ErrNotFound，别悄悄成功。
	if _, err := st.HideSocialPhoto(999999); err != ErrNotFound {
		t.Errorf("不存在的 id 应回 ErrNotFound，得到 %v", err)
	}
	// 幂等：再删一次同一张仍然成功（前端连点两下不该报错）。
	if _, err := st.HideSocialPhoto(target); err != nil {
		t.Errorf("重复删除应成功，得到 %v", err)
	}
}

func TestListMomentsOrderAndCursor(t *testing.T) {
	st := newTestStore(t)
	acc, err := st.UpsertSocialAccount(&SocialAccount{Platform: "x", Handle: "mini"})
	if err != nil {
		t.Fatal(err)
	}
	day1 := time.Date(2026, 10, 23, 10, 0, 0, 0, time.UTC)
	day2 := day1.Add(-24 * time.Hour)
	// 一条帖子两张图（carousel），另一条帖子一张图。
	posts := []SocialPostInput{
		{PostID: "b", URL: "u-b", PostedAt: day1, Photos: []SocialPhotoInput{
			{URL: "u-b-1", Seq: 1}, {URL: "u-b-2", Seq: 2},
		}},
		{PostID: "a", URL: "u-a", PostedAt: day2, Photos: []SocialPhotoInput{{URL: "u-a-1", Seq: 1}}},
	}
	if _, err := st.InsertSocialPosts(acc.ID, "x", posts); err != nil {
		t.Fatal(err)
	}

	page, next, err := st.ListMoments(MomentsQuery{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(page) != 2 || next == "" {
		t.Fatalf("首页应有 2 条且带游标，得到 %d 条 / %q", len(page), next)
	}
	// 新帖在前，帖内按 seq 正序（灯箱里先走完同帖）。
	if page[0].URL != "u-b-1" || page[1].URL != "u-b-2" {
		t.Errorf("顺序不对: %s, %s", page[0].URL, page[1].URL)
	}

	page2, next2, err := st.ListMoments(MomentsQuery{Limit: 2, Cursor: next})
	if err != nil {
		t.Fatal(err)
	}
	if len(page2) != 1 || page2[0].URL != "u-a-1" {
		t.Fatalf("第二页应只剩 u-a-1，得到 %+v", page2)
	}
	if next2 != "" {
		t.Errorf("到底了不该给游标，得到 %q", next2)
	}

	// 按博主筛选
	other, _ := st.UpsertSocialAccount(&SocialAccount{Platform: "instagram", Handle: "other"})
	if _, err := st.InsertSocialPosts(other.ID, "instagram", []SocialPostInput{
		{PostID: "zzz", URL: "u-z", PostedAt: day1, Photos: []SocialPhotoInput{{URL: "u-z-1", Seq: 1}}},
	}); err != nil {
		t.Fatal(err)
	}
	only, _, err := st.ListMoments(MomentsQuery{AccountID: other.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(only) != 1 || only[0].URL != "u-z-1" {
		t.Errorf("按博主筛选失败: %+v", only)
	}
}

func TestLinkSocialAccountsToPerformers(t *testing.T) {
	st := newTestStore(t)
	perfID, err := st.UpsertPerformer(&model.Performer{
		Platform: "pornhub", PerformerID: "mini-diva", Name: "Mini Diva",
		URL: "https://cn.pornhub.com/model/mini-diva",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.UpdatePerformerProfile(&model.Performer{
		ID: perfID,
		Social: []model.SocialLink{
			{Label: "Twitter", URL: "https://twitter.com/Mini_Diva?lang=en"},
			{Label: "Instagram", URL: "https://www.instagram.com/mini.diva/"},
		},
	}); err != nil {
		t.Fatalf("写入社交链接失败: %v", err)
	}

	x, err := st.UpsertSocialAccount(&SocialAccount{Platform: "x", Handle: "mini_diva"})
	if err != nil {
		t.Fatal(err)
	}
	ig, err := st.UpsertSocialAccount(&SocialAccount{Platform: "instagram", Handle: "mini.diva"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.LinkSocialAccountsToPerformers(); err != nil {
		t.Fatal(err)
	}
	gotX, _ := st.GetSocialAccount(x.ID)
	gotIG, _ := st.GetSocialAccount(ig.ID)
	if gotX.PerformerID != perfID {
		t.Errorf("X 账号没关联上艺人: %d != %d", gotX.PerformerID, perfID)
	}
	if gotIG.PerformerID != perfID {
		t.Errorf("IG 账号没关联上艺人: %d != %d", gotIG.PerformerID, perfID)
	}
}

func TestNormalizeSocialURL(t *testing.T) {
	cases := []struct {
		in       string
		platform string
		handle   string
		ok       bool
	}{
		{"https://twitter.com/Mini_Diva", "x", "mini_diva", true},
		{"https://x.com/mini/", "x", "mini", true},
		{"https://www.instagram.com/mini.diva/?hl=zh-cn", "instagram", "mini.diva", true},
		{"https://www.instagram.com/p/ABC123/", "", "", false},
		{"https://example.com/mini", "", "", false},
		{"", "", "", false},
	}
	for _, c := range cases {
		platform, handle, ok := NormalizeSocialURL(c.in)
		if ok != c.ok || platform != c.platform || handle != c.handle {
			t.Errorf("NormalizeSocialURL(%q) = (%q,%q,%v)，期望 (%q,%q,%v)",
				c.in, platform, handle, ok, c.platform, c.handle, c.ok)
		}
	}
}

func TestFindPerformerBySocialAndPerformerMoments(t *testing.T) {
	st := newTestStore(t)
	perfID, err := st.UpsertPerformer(&model.Performer{
		Platform: "pornhub", PerformerID: "mini-diva", Name: "Mini Diva",
		URL: "https://cn.pornhub.com/model/mini-diva",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.UpdatePerformerProfile(&model.Performer{
		ID:     perfID,
		Social: []model.SocialLink{{Label: "Twitter", URL: "https://twitter.com/Mini_Diva"}},
	}); err != nil {
		t.Fatal(err)
	}

	pid, name, ok := st.FindPerformerBySocial("x", "mini_diva")
	if !ok || pid != perfID || name != "Mini Diva" {
		t.Fatalf("按社交链接反查艺人失败: %d %q %v", pid, name, ok)
	}
	if _, _, ok := st.FindPerformerBySocial("instagram", "mini_diva"); ok {
		t.Error("没有 IG 链接时不该匹配上")
	}

	acc, err := st.UpsertSocialAccount(&SocialAccount{Platform: "x", Handle: "mini_diva"})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetSocialAccountPerformer(acc.ID, perfID); err != nil {
		t.Fatal(err)
	}
	linked, err := st.ListSocialAccountsByPerformer(perfID)
	if err != nil || len(linked) != 1 || linked[0].ID != acc.ID {
		t.Fatalf("按艺人列账号失败: %v %+v", err, linked)
	}

	// 另一位的账号不该混进来
	other, _ := st.UpsertSocialAccount(&SocialAccount{Platform: "x", Handle: "other"})
	if _, err := st.InsertSocialPosts(other.ID, "x", []SocialPostInput{
		{PostID: "zz", URL: "u-z", PostedAt: time.Now(), Photos: []SocialPhotoInput{{URL: "p-z", Seq: 1}}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.InsertSocialPosts(acc.ID, "x", []SocialPostInput{
		{PostID: "aa", URL: "u-a", PostedAt: time.Now(), Photos: []SocialPhotoInput{
			{URL: "p-a1", Seq: 1}, {URL: "p-a2", Seq: 2},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	photos, _, err := st.ListMoments(MomentsQuery{PerformerID: perfID})
	if err != nil {
		t.Fatal(err)
	}
	if len(photos) != 2 {
		t.Fatalf("艺人名下应有 2 张照片，得到 %d", len(photos))
	}
	for _, p := range photos {
		if p.Account.ID != acc.ID {
			t.Errorf("混进了别的账号的照片: %+v", p.Account)
		}
	}

	// 解除关联后两边都空了
	if err := st.SetSocialAccountPerformer(acc.ID, 0); err != nil {
		t.Fatal(err)
	}
	if linked, _ := st.ListSocialAccountsByPerformer(perfID); len(linked) != 0 {
		t.Errorf("解除后不该还挂着账号: %+v", linked)
	}
	if photos, _, _ := st.ListMoments(MomentsQuery{PerformerID: perfID}); len(photos) != 0 {
		t.Errorf("解除后艺人名下不该还有照片，得到 %d 张", len(photos))
	}
}

func TestDeleteSocialAccountCascades(t *testing.T) {
	st := newTestStore(t)
	acc, _ := st.UpsertSocialAccount(&SocialAccount{Platform: "x", Handle: "mini"})
	ids, err := st.InsertSocialPosts(acc.ID, "x", []SocialPostInput{
		{PostID: "111", URL: "u", PostedAt: time.Now(), Photos: []SocialPhotoInput{{URL: "p1", Seq: 1}}},
	})
	if err != nil || len(ids) != 1 {
		t.Fatalf("准备数据失败: %v", err)
	}
	if err := st.DeleteSocialAccount(acc.ID); err != nil {
		t.Fatal(err)
	}
	stats, err := st.SocialStats()
	if err != nil {
		t.Fatal(err)
	}
	if stats.Accounts != 0 || stats.Posts != 0 || stats.Photos != 0 {
		t.Errorf("删博主应级联清掉帖子和照片，仍有 %+v", stats)
	}
}
