package pornhub

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestModelSlugCandidates(t *testing.T) {
	cases := []struct {
		name string
		want []string
	}{
		// beeg 上常见的连写：两种都要试
		{"MiniDiva", []string{"minidiva"}},
		{"Mini Diva", []string{"mini-diva", "minidiva"}},
		{"Riley Reid", []string{"riley-reid", "rileyreid"}},
		// 标点、多余空格、下划线都要收拾干净
		{"  Riley   Reid ", []string{"riley-reid", "rileyreid"}},
		{"Mia_Khalifa!", []string{"mia-khalifa", "miakhalifa"}},
		{"Busty-Amelia", []string{"busty-amelia", "bustyamelia"}},
		{"", nil},
		{"!!!", nil},
	}
	for _, c := range cases {
		got := modelSlugCandidates(c.name)
		if len(got) != len(c.want) {
			t.Errorf("%q → %v，想要 %v", c.name, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("%q → %v，想要 %v", c.name, got, c.want)
				break
			}
		}
	}
}

func TestSamePerson(t *testing.T) {
	yes := [][2]string{
		{"Mini Diva", "MiniDiva"},
		{"Riley Reid", "riley reid"},
		{"Mia_Khalifa", "Mia Khalifa"},
		{"Lana Rhoades", "lana rhoades "},
	}
	for _, c := range yes {
		if !samePerson(c[0], c[1]) {
			t.Errorf("%q 与 %q 应该是同一个人", c[0], c[1])
		}
	}
	no := [][2]string{
		{"Mini Diva", "Mini Divan"},
		{"Riley Reid", "Riley Reed"},
		{"", "Riley Reid"},
	}
	for _, c := range no {
		if samePerson(c[0], c[1]) {
			t.Errorf("%q 与 %q 不该判成同一个人", c[0], c[1])
		}
	}
}

// 艳星搜索结果卡片的解析：名字常被拆进 .lastName，头像在 img.pornstarThumb 上。
func TestParsePornstarCards(t *testing.T) {
	body := []byte(`<html><body>
	<ul class="pornstars">
	  <li class="performerCard">
	    <a href="/pornstar/riley-reid">
	      <img class="pornstarThumb" src="https://ei.phncdn.com/pics/users/0001/avatar.jpg" alt="Riley Reid">
	      <span class="pornstar_label"><span class="title-album"><span class="rank_number">12</span></span></span>
	    </a>
	    <div class="pornstarNameIcon">
	      <span class="pornStarName performerCardName">
	        <a href="/pornstar/riley-reid" rel="nofollow">Riley <span class="lastName">Reid</span></a>
	      </span>
	    </div>
	  </li>
	  <li class="performerCard">
	    <a href="/model/mini-diva"><img class="pornstarThumb" data-thumb_url="https://ei.phncdn.com/av2.jpg" alt="Mini Diva"></a>
	    <div class="pornstarNameIcon"><span class="pornStarName performerCardName"><a href="/model/mini-diva">Mini Diva</a></span></div>
	  </li>
	  <li class="performerCard"><a href="/pornstar/riley-reid">重复的卡片</a></li>
	</ul></body></html>`)

	items := parsePornstarCards(body, "https://cn.pornhub.com")
	if len(items) != 2 {
		t.Fatalf("应解析出 2 位（重复的去重），得到 %d: %+v", len(items), items)
	}
	first := items[0]
	if first.PerformerID != "riley-reid" || first.Name != "Riley Reid" {
		t.Errorf("第一个卡片解析错了：%+v", first)
	}
	if first.URL != "https://cn.pornhub.com/pornstar/riley-reid" {
		t.Errorf("URL 没补全：%q", first.URL)
	}
	if first.AvatarURL != "https://ei.phncdn.com/pics/users/0001/avatar.jpg" {
		t.Errorf("头像抓错了：%q", first.AvatarURL)
	}
	if second := items[1]; second.Name != "Mini Diva" || second.AvatarURL != "https://ei.phncdn.com/av2.jpg" {
		t.Errorf("第二个卡片解析错了：%+v", second)
	}
}

// 搜索结果的卡片是另一套结构（li > .wrap > a > img[data-thumb_url] + a.title）。
func TestParsePornstarCardsSearchResults(t *testing.T) {
	body := []byte(`<html><body><ul id="pornstarsSearchResult">
	  <li>
	    <div class="wrap">
	      <a href="/pornstar/daisy-diva">
	        <span class="pornstar_label"><span class="title-album">Rank: <span class="rank_number">8505</span></span></span>
	        <img data-thumb_url="https://ei.phncdn.com/daisy.jpg" alt="Daisy Diva"/>
	      </a>
	      <div class="thumbnail-info-wrapper">
	        <a href="/pornstar/daisy-diva" class="title">Daisy Diva <span class="hidden">thumbnail</span></a>
	        <span class="videosNumber">13 Videos</span>
	      </div>
	    </div>
	  </li>
	  <li>
	    <div class="wrap">
	      <a href="/pornstar/mini-vampp"><img data-thumb_url="https://ei.phncdn.com/vampp.jpg"/></a>
	      <div class="thumbnail-info-wrapper"><a href="/pornstar/mini-vampp" class="title">Mini Vampp</a></div>
	    </div>
	  </li>
	  <li class="pcVideoListItem">
	    <div class="thumbnail-info-wrapper">
	      <a href="/view_video.php?viewkey=abc123" class="title">Hot scene with Mini Vampp</a>
	    </div>
	    <a href="/model/mini-vampp">Uploader</a>
	  </li>
	</ul></body></html>`)

	items := parsePornstarCards(body, "https://cn.pornhub.com")
	if len(items) != 2 {
		t.Fatalf("应解析出 2 位，得到 %d: %+v", len(items), items)
	}
	if items[0].PerformerID != "daisy-diva" || items[0].Name != "Daisy Diva" ||
		items[0].AvatarURL != "https://ei.phncdn.com/daisy.jpg" {
		t.Errorf("结果卡片解析错了：%+v", items[0])
	}
	if items[1].Name != "Mini Vampp" {
		t.Errorf("没有 a.title 时应退回 img 的 alt：%+v", items[1])
	}
}

// TestLiveResolveProfile 是可选的真实网络测试（LIVE=1 才跑）：
// 确认 Pornhub 上按名字找得到人，且资料字段（简介 / 标签 / 社交链接 / 排名）仍然解析得出来。
func TestLiveResolveProfile(t *testing.T) {
	if os.Getenv("LIVE") == "" {
		t.Skip("设置 LIVE=1 才跑真实网络测试")
	}
	names := []string{"Mini Diva", "Riley Reid"}
	if custom := os.Getenv("LIVE_NAME"); custom != "" {
		names = []string{custom}
	}
	a, err := New("https://cn.pornhub.com", "", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		p, err := a.ResolveProfile(ctx, name)
		cancel()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		t.Logf("%s → %q @%s/%s 简介 %d 字 标签 %d 行 社交 %d 条 排名 %d 头像 %s",
			name, p.Name, p.Platform, p.PerformerID, len([]rune(p.Bio)),
			len(p.Profile), len(p.Social), p.Rank, p.AvatarURL)
		for _, f := range p.Profile {
			t.Logf("    %s: %s", f.Label, f.Value)
		}
		for _, s := range p.Social {
			t.Logf("    %s → %s", s.Label, s.URL)
		}
		if p.Name == "" || p.Bio == "" {
			t.Errorf("%s: 资料没抓全：%+v", name, p)
		}
	}
}
