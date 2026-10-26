package jobs

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/mario/myporn/internal/model"
	"github.com/mario/myporn/internal/platform"
	"github.com/mario/myporn/internal/store"
)

// fakeAvatarSource 顶替真实的头像来源，记录被调用了几次。
type fakeAvatarSource struct {
	key  string
	url  string
	err  error
	hits int
}

func (f *fakeAvatarSource) Key() string { return f.key }

func (f *fakeAvatarSource) ResolveAvatar(_ context.Context, name string) (string, error) {
	f.hits++
	if f.err != nil {
		return "", f.err
	}
	return f.url, nil
}

func newAvatarFixture(t *testing.T, sources ...platform.AvatarProvider) (*store.Store, *AvatarFiller) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "avatar.db"))
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	reg := platform.NewAvatarRegistry()
	for _, s := range sources {
		reg.Register(s)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return st, NewAvatarFiller(st, reg, log)
}

func TestAvatarFillWritesBackAndStopsAsking(t *testing.T) {
	src := &fakeAvatarSource{key: "beeg", url: "https://thumbs.example.com/photos/1/to.webp?crop_id=2"}
	st, filler := newAvatarFixture(t, src)
	id, err := st.UpsertPerformer(&model.Performer{Platform: "pornhub", PerformerID: "mini-diva",
		Name: "Mini Diva", URL: "https://example.invalid/model/mini-diva"})
	if err != nil {
		t.Fatal(err)
	}
	p, _ := st.GetPerformer(id)

	if got := filler.Fill(context.Background(), p); got != src.url {
		t.Fatalf("Fill 返回 %q, 期望 %q", got, src.url)
	}
	saved, _ := st.GetPerformer(id)
	if saved.AvatarURL != src.url {
		t.Fatalf("头像没写回数据库: %q", saved.AvatarURL)
	}
	// 再补一次：已经有头像了，不该再去问来源。
	if got := filler.Fill(context.Background(), saved); got != src.url {
		t.Fatalf("第二次 Fill 返回 %q", got)
	}
	if src.hits != 1 {
		t.Fatalf("头像源被问了 %d 次，应当只问一次", src.hits)
	}
}

func TestAvatarFillFallsThroughToNextSource(t *testing.T) {
	broken := &fakeAvatarSource{key: "broken", err: errors.New("http 500")}
	good := &fakeAvatarSource{key: "beeg", url: "https://img/b.webp"}
	st, filler := newAvatarFixture(t, broken, good)
	id, _ := st.UpsertPerformer(&model.Performer{Platform: "pornhub", PerformerID: "p", Name: "某人", URL: "https://x/p"})
	p, _ := st.GetPerformer(id)
	if got := filler.Fill(context.Background(), p); got != good.url {
		t.Fatalf("应当回落到第二个来源，得到 %q", got)
	}
	if broken.hits != 1 || good.hits != 1 {
		t.Fatalf("调用次数不对: broken=%d good=%d", broken.hits, good.hits)
	}
}

func TestAvatarFillCoolsDownAfterMiss(t *testing.T) {
	src := &fakeAvatarSource{key: "beeg", err: errors.New("beeg 上没有艺人")}
	st, filler := newAvatarFixture(t, src)
	id, _ := st.UpsertPerformer(&model.Performer{Platform: "pornhub", PerformerID: "p", Name: "查不到", URL: "https://x/p"})
	p, _ := st.GetPerformer(id)

	if got := filler.Fill(context.Background(), p); got != "" {
		t.Fatalf("查不到应当返回空串，得到 %q", got)
	}
	// 冷却期内再点一次页面，不该又去打一次接口。
	if got := filler.Fill(context.Background(), p); got != "" {
		t.Fatalf("第二次也应为空，得到 %q", got)
	}
	if src.hits != 1 {
		t.Fatalf("冷却期内重复请求了 %d 次", src.hits)
	}
}

func TestAvatarFillMissing(t *testing.T) {
	src := &fakeAvatarSource{key: "beeg", url: "https://img/x.webp"}
	st, filler := newAvatarFixture(t, src)
	for _, id := range []string{"a", "b"} {
		if _, err := st.UpsertPerformer(&model.Performer{Platform: "pornhub", PerformerID: id,
			Name: "艺人" + id, URL: "https://x/" + id}); err != nil {
			t.Fatal(err)
		}
	}
	checked, filled, err := filler.FillMissing(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if checked != 2 || filled != 2 {
		t.Fatalf("checked=%d filled=%d, 期望 2/2", checked, filled)
	}
	left, _ := st.ListPerformersWithoutAvatar()
	if len(left) != 0 {
		t.Fatalf("还有 %d 位没补上", len(left))
	}
}

func TestAvatarFillerWithoutSourcesIsNoop(t *testing.T) {
	st, filler := newAvatarFixture(t)
	id, _ := st.UpsertPerformer(&model.Performer{Platform: "pornhub", PerformerID: "p", Name: "某人", URL: "https://x/p"})
	p, _ := st.GetPerformer(id)
	if got := filler.Fill(context.Background(), p); got != "" {
		t.Fatalf("没有来源时应当返回空串，得到 %q", got)
	}
	if got := filler.Lookup(context.Background(), "某人"); got != "" {
		t.Fatalf("没有来源时 Lookup 应当返回空串，得到 %q", got)
	}
}
