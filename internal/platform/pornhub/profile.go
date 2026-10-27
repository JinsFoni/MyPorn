package pornhub

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"unicode"

	"github.com/mario/myporn/internal/model"
	"github.com/mario/myporn/internal/platform"
)

// ResolveProfile 按艺人名在 Pornhub 上找到同一个人，返回整份资料。
//
// beeg / xHamster 的艺人页资料很薄（名字 + 头像 + 两个站内统计），
// 这里把 Pornhub 上那份完整档案借过去：简介、资料标签、三围、社交链接、排名。
// 两条路：
//  1. 先按名字推主页标识直接取页面（一两个请求，最快）；
//  2. 标识猜不中再走艳星搜索，结果里挑**名字完全对得上**的那一位。
//
// 名字对不上就报错返回，绝不"取第一个"——给别人安错简介比留空糟得多。
func (a *Adapter) ResolveProfile(ctx context.Context, performerName string) (*model.Performer, error) {
	name := strings.TrimSpace(performerName)
	if normalizeName(name) == "" {
		return nil, errors.New("艺人名为空，没法在 Pornhub 上找")
	}

	var firstErr error
	for _, slug := range modelSlugCandidates(name) {
		p, err := a.getProfileBySlug(ctx, slug)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if samePerson(p.Name, name) {
			return p, nil
		}
		if firstErr == nil {
			firstErr = fmt.Errorf("Pornhub 的 /model/%s 是 %q，不是 %q", slug, p.Name, name)
		}
	}

	p, err := a.profileFromSearch(ctx, name)
	if err == nil {
		return p, nil
	}
	if firstErr != nil {
		return nil, fmt.Errorf("Pornhub 上没找到同名艺人（%v；搜索: %v）", firstErr, err)
	}
	return nil, err
}

// getProfileBySlug 抓一位艺人的资料页。
// 注意 /model/<slug> 对认证艳星会 301 到 /pornstar/<slug>，
// 普通用户会 301 到 /users/<slug>（页面结构不同，能解析出多少算多少）。
func (a *Adapter) getProfileBySlug(ctx context.Context, slug string) (*model.Performer, error) {
	body, err := a.get(ctx, "/model/"+url.PathEscape(slug), nil)
	if err != nil {
		return nil, err
	}
	p, err := parsePerformerPage(body)
	if err != nil {
		return nil, err
	}
	p.Platform = a.Key()
	p.PerformerID = slug
	p.URL = a.base + "/model/" + slug
	return p, nil
}

// profileFromSearch 用艳星搜索兜底：只在结果里名字（或标识）完全对得上时采用。
func (a *Adapter) profileFromSearch(ctx context.Context, name string) (*model.Performer, error) {
	items, _, err := a.SearchPerformers(ctx, name, 1)
	if err != nil {
		return nil, err
	}
	wantSlug := normalizeName(name)
	for _, it := range items {
		if !samePerson(it.Name, name) && !sameSlug(it.PerformerID, wantSlug) {
			continue
		}
		return a.getProfileBySlug(ctx, it.PerformerID)
	}
	return nil, fmt.Errorf("搜索没有同名结果")
}

// modelSlugCandidates 由艺人名推出可能的 Pornhub 主页标识。
// 站点口径是「小写、空格换连字符、去标点」，但各家把它写成什么样都有：
// beeg 上常见的连写 MiniDiva 要同时试 minidiva 和 mini-diva。
func modelSlugCandidates(name string) []string {
	lower := strings.ToLower(strings.TrimSpace(name))
	if lower == "" {
		return nil
	}
	var hyphen, plain strings.Builder
	lastHyphen := true // 开头的连字符不要
	for _, r := range lower {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			hyphen.WriteRune(r)
			plain.WriteRune(r)
			lastHyphen = false
		case unicode.IsSpace(r) || r == '-' || r == '_' || r == '.':
			if !lastHyphen {
				hyphen.WriteByte('-')
				lastHyphen = true
			}
		}
	}
	out := make([]string, 0, 2)
	seen := map[string]bool{}
	for _, s := range []string{strings.Trim(hyphen.String(), "-"), strings.Trim(plain.String(), "-")} {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

// normalizeName 只留字母和数字，用来判断两个写法是不是同一个人：
// "Mini Diva"、"MiniDiva"、"mini_diva" 都归一成 "minidiva"。
func normalizeName(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// samePerson 判断站外页面上的名字是不是要找的那位艺人。
func samePerson(pageName, want string) bool {
	a, b := normalizeName(pageName), normalizeName(want)
	return a != "" && a == b
}

// sameSlug 判断搜索结果里的标识是不是要找的那位（连字符已去掉后再比）。
func sameSlug(slug, wantPlain string) bool {
	s := strings.ReplaceAll(strings.ToLower(strings.TrimSpace(slug)), "-", "")
	return s != "" && wantPlain != "" && s == wantPlain
}

// 编译期检查：Pornhub 满足 platform.ProfileProvider（beeg / xHamster 的资料来源）。
var _ platform.ProfileProvider = (*Adapter)(nil)
