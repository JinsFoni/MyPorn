package store

import "testing"

// ListSubscriptions 的两个排除层面：
//  1. B 站 UP 主**永远**不在里面（它们走 /api/bilibili/channels 那一套）；
//  2. 调用方可以额外排掉几个平台——关注页头像条排 krx18：关注流本身不收 krx18 的片子，
//     拿那几个 tag 当筛选项只会筛出空列表。
func TestListSubscriptionsExcludePlatform(t *testing.T) {
	st := newTestStore(t)
	phID := mustPerformer(t, st, "pornhub", "mini-diva", "Mini Diva")
	krxID := mustPerformer(t, st, "krx18", "do-hee-도희", "Do Hee (도희)")
	biliID := mustPerformer(t, st, "bilibili", "946974", "影视飓风")
	for _, id := range []int64{phID, krxID, biliID} {
		if _, err := st.CreateSubscription(id, 0); err != nil {
			t.Fatal(err)
		}
	}

	all, err := st.ListSubscriptions()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("不传排除时该回 pornhub + krx18 两条（B 站永不在里面），实际 %d 条", len(all))
	}
	for _, p := range all {
		if p.Platform == "bilibili" {
			t.Fatalf("B 站不该出现在订阅列表里：%+v", p)
		}
	}

	feed, err := st.ListSubscriptions("krx18")
	if err != nil {
		t.Fatal(err)
	}
	if len(feed) != 1 || feed[0].Platform != "pornhub" {
		t.Fatalf("排掉 krx18 后该只剩 pornhub，实际 %+v", feed)
	}

	// 排一个不存在的平台不能把列表排空（NOT IN 只该多一条条件）
	same, err := st.ListSubscriptions("nonexistent")
	if err != nil {
		t.Fatal(err)
	}
	if len(same) != 2 {
		t.Fatalf("排一个不存在的平台不该影响结果，实际 %d 条", len(same))
	}
}
