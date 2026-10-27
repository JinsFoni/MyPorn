package model

import (
	"regexp"
	"strings"
)

// AdoptProfileFrom 用站外资料来源（目前是 Pornhub）的资料内容覆盖本地这份，
// 返回是否真的改动了什么。
//
// 换的：简介、资料标签行、三围、社交链接、模特排名。
// 不换的：平台、平台标识、主页链接、名字、影片数、订阅数——前三个是本站在哪儿
// 追更的依据，后两个是**本站这个平台**的统计，拿 Pornhub 的数字顶上去会把
// 1.4M 订阅挂在 beeg 的艺人页上。
//
// 三条细则：src 里为空/为 0 的字段不动本地（跟 UpsertPerformer 里 COALESCE
// 的口径一致）；头像只在本地为空时才借鉴——本站自己的头像是站点正在用的那张，
// 比从别人家页面上抠的准（Pornhub 的头像换版就抓不到）；资料来源的简介如果
// 只是一串网址（Pornhub 上不少老艺人就写个 OnlyFans 地址），也不覆盖。
func (p *Performer) AdoptProfileFrom(src *Performer) bool {
	if p == nil || src == nil {
		return false
	}
	changed := false
	if bio := strings.TrimSpace(src.Bio); bio != "" && bio != p.Bio && !bioIsJustALink(bio) {
		p.Bio = bio
		changed = true
	}
	if len(src.Profile) > 0 && !profileFieldsEqual(p.Profile, src.Profile) {
		p.Profile = src.Profile
		changed = true
	}
	if !src.Body.Empty() && src.Body != p.Body {
		p.Body = src.Body
		changed = true
	}
	if len(src.Social) > 0 && !socialLinksEqual(p.Social, src.Social) {
		p.Social = src.Social
		changed = true
	}
	if src.Rank > 0 && src.Rank != p.Rank {
		p.Rank = src.Rank
		changed = true
	}
	if strings.TrimSpace(p.AvatarURL) == "" {
		if url := strings.TrimSpace(src.AvatarURL); url != "" {
			p.AvatarURL = url
			changed = true
		}
	}
	return changed
}

func profileFieldsEqual(a, b []ProfileField) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func socialLinksEqual(a, b []SocialLink) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// urlishRe 认出简介里的网址：带协议的、带 www. 的，以及裸域名（onlyfans.com/xxx）。
var urlishRe = regexp.MustCompile(`(?i)(?:https?://|www\.)\S+|\b[\w-]+\.(?:com|net|org|xxx|tv|me|co|link|cc|vip)\S*`)

// bioIsJustALink 判断简介是不是「只有一串网址」。
// Pornhub 上不少老艺人的简介就写个 OnlyFans 地址（Riley Reid 就是
// "Onlyfans.com/rileyreidx3"），拿它盖掉本网站上那句真简介是净亏；
// 网址本来就挂在社交链接里，不差这一份。去掉网址后还剩不到两个词的，就算。
func bioIsJustALink(s string) bool {
	if !urlishRe.MatchString(s) {
		return false
	}
	rest := urlishRe.ReplaceAllString(s, " ")
	return len(strings.Fields(rest)) < 2
}
