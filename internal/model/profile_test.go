package model

import "testing"

func TestAdoptProfileFrom(t *testing.T) {
	local := &Performer{
		Platform:        "beeg",
		PerformerID:     "MiniDiva",
		Name:            "MiniDiva",
		URL:             "https://beeg.com/MiniDiva",
		AvatarURL:       "https://externulls.com/thumb/minidiva.jpg",
		Bio:             "beeg 上那句很短的简介",
		VideoCount:      intPtr(48),
		SubscriberCount: intPtr(1200),
	}
	src := &Performer{
		Platform:    "pornhub",
		PerformerID: "mini-diva",
		Name:        "Mini Diva",
		URL:         "https://cn.pornhub.com/model/mini-diva",
		AvatarURL:   "https://ei.phncdn.com/avatar.jpg",
		Bio:         "Pornhub 上的完整简介",
		Profile:     []ProfileField{{Label: "性别", Value: "女性"}, {Label: "出身地", Value: "美国"}},
		Body:        Body{HeightCm: 167, Bust: 89, Waist: 64, Hips: 91},
		Social:      []SocialLink{{Label: "Twitter", URL: "https://twitter.com/minidiva"}},
		Rank:        269,
	}

	if !local.AdoptProfileFrom(src) {
		t.Fatal("应该有改动")
	}
	// 内容换掉
	if local.Bio != src.Bio {
		t.Errorf("简介没换：%q", local.Bio)
	}
	if len(local.Profile) != 2 || local.Rank != 269 || local.Body.HeightCm != 167 {
		t.Errorf("资料区块没换：%+v rank=%d body=%+v", local.Profile, local.Rank, local.Body)
	}
	if len(local.Social) != 1 {
		t.Errorf("社交链接没换：%+v", local.Social)
	}
	// 本站的东西留着：平台标识、主页链接、名字、平台统计、已有头像
	if local.Platform != "beeg" || local.PerformerID != "MiniDiva" || local.Name != "MiniDiva" {
		t.Errorf("本站标识被改了：%s/%s/%s", local.Platform, local.PerformerID, local.Name)
	}
	if local.URL != "https://beeg.com/MiniDiva" {
		t.Errorf("主页链接被改了：%s", local.URL)
	}
	if local.VideoCount == nil || *local.VideoCount != 48 || local.SubscriberCount == nil || *local.SubscriberCount != 1200 {
		t.Errorf("平台统计被改了：%v / %v", local.VideoCount, local.SubscriberCount)
	}
	if local.AvatarURL != "https://externulls.com/thumb/minidiva.jpg" {
		t.Errorf("本站头像应保留：%s", local.AvatarURL)
	}

	// 已经一样了：再借一次不算改动，免得每次同步都白写一次库。
	if local.AdoptProfileFrom(src) {
		t.Error("同一份资料不该再算改动")
	}
}

func TestAdoptProfileFromFillsEmptyAvatar(t *testing.T) {
	local := &Performer{Platform: "xhamster", PerformerID: "riley-reid", Name: "Riley Reid"}
	src := &Performer{AvatarURL: "https://ei.phncdn.com/avatar.jpg", Bio: "简介"}
	if !local.AdoptProfileFrom(src) {
		t.Fatal("应该补上头像与简介")
	}
	if local.AvatarURL != src.AvatarURL {
		t.Errorf("空头像应借用：%q", local.AvatarURL)
	}
}

func TestAdoptProfileFromIgnoresEmptySource(t *testing.T) {
	local := &Performer{
		Platform: "beeg", PerformerID: "x", Name: "X",
		Bio:     "原简介",
		Profile: []ProfileField{{Label: "认证", Value: "是"}},
		Rank:    12,
		Body:    Body{HeightCm: 170},
	}
	if local.AdoptProfileFrom(&Performer{Name: "X"}) {
		t.Error("空的资料来源不该产生改动")
	}
	if local.Bio != "原简介" || len(local.Profile) != 1 || local.Rank != 12 || local.Body.HeightCm != 170 {
		t.Errorf("原有资料被清掉了：%+v", local)
	}
	if local.AdoptProfileFrom(nil) {
		t.Error("nil 资料来源不该产生改动")
	}
}

// 资料来源的简介如果只是串网址，别覆盖本网站上那句真简介。
func TestAdoptProfileFromKeepsRealBioOverLinkOnly(t *testing.T) {
	local := &Performer{Bio: "xHamster 上那段 500 字的真简介"}
	src := &Performer{Bio: "Onlyfans.com/rileyreidx3"}
	if local.AdoptProfileFrom(src) {
		t.Error("只有网址的简介不该产生改动")
	}
	if local.Bio != "xHamster 上那段 500 字的真简介" {
		t.Errorf("真简介被盖掉了：%q", local.Bio)
	}

	// 带网址但还有正文的简介是正常的，照常采用。
	src.Bio = "Love being here from time to time, blog: https://onlyfans.com/minidivaonly"
	if !local.AdoptProfileFrom(src) || local.Bio != src.Bio {
		t.Errorf("带正文的简介应被采用：%q", local.Bio)
	}
}

func TestBioIsJustALink(t *testing.T) {
	yes := []string{
		"Onlyfans.com/rileyreidx3",
		"https://onlyfans.com/x",
		"www.example.com",
		"  onlyfans.com/abc  ",
	}
	for _, s := range yes {
		if !bioIsJustALink(s) {
			t.Errorf("%q 应判成纯网址", s)
		}
	}
	no := []string{
		"Love being here from time to time.",
		"Blog: https://onlyfans.com/minidivaonly — come say hi",
		"",
		"Hi there!",
	}
	for _, s := range no {
		if bioIsJustALink(s) {
			t.Errorf("%q 不该判成纯网址", s)
		}
	}
}

// 「职业状态 / 工作起始及结束时间」任何平台都不入库。
func TestDropProfileLabel(t *testing.T) {
	drop := []string{
		"职业状态", "工作起始及结束时间", "Career Status", "career start and end time",
		" 职业状态 ",
	}
	for _, l := range drop {
		if !DropProfileLabel(l) {
			t.Errorf("%q 应被丢掉", l)
		}
	}
	keep := []string{"感情状态", "性别", "出身地", "发色", "身高", "Career", "状态", ""}
	for _, l := range keep {
		if DropProfileLabel(l) {
			t.Errorf("%q 不该被丢掉", l)
		}
	}
}

func TestCleanProfile(t *testing.T) {
	in := []ProfileField{
		{Label: "感情状态", Value: "单身"},
		{Label: "职业状态", Value: "活跃的"},
		{Label: "工作起始及结束时间", Value: "2011 至 今"},
		{Label: "性别", Value: "女性"},
	}
	out := CleanProfile(in)
	if len(out) != 2 || out[0].Label != "感情状态" || out[1].Label != "性别" {
		t.Fatalf("应只剩 2 行且顺序不变，实际 %+v", out)
	}
	if len(in) != 4 {
		t.Error("不该改动入参")
	}
	if CleanProfile(nil) != nil {
		t.Error("空输入应原样返回")
	}
}

func intPtr(n int) *int { return &n }
