package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/JinsFoni/MyPorn/internal/platform/bilibili"
)

// errNoAdapter 表示 B 站适配器这一轮还没装好（例如 base URL 写错）。
var errNoAdapter = errors.New("B 站适配器未就绪")

// biliAdapter 取 B 站适配器（扫码与续期都要它的 HTTP 客户端和 Cookie 策略）。
func (s *Server) biliAdapter() (*bilibili.Adapter, bool) {
	ad, ok := s.reg.Get(platformBilibili)
	if !ok {
		return nil, false
	}
	bl, ok := ad.(*bilibili.Adapter)
	return bl, ok
}

// 本文件：B 站登录态的两条出路——扫码登录（一次性）和自动续期（长期）。
//
// 为什么要有这两条（2026-10-28~30 的两次实测）：
//   - 手填 Cookie 拿不到 HttpOnly 的 SESSDATA，照着 document.cookie 抄来的串一律 -101；
//   - 手填的串里没有 refresh_token，B 站一旦作废登录态（实测在 SESSDATA 内嵌到期
//     2027-04-25 的情况下第 5 天就 -101）就只能重填。
//
// 所以：扫码把五个字段一次拿到（含 refresh_token），之后由 CheckAndRefreshBiliCredential
// 每几小时问一次 cookie/info，到点就续——只要还在用，这条登录态就不会过期。
const (
	// biliRefreshTokenKey 存 refresh_token（浏览器那边叫 ac_time_value）。手填 Cookie 没有它，
	// 这个槽为空时界面要明确说「只能重填，不能自动续」。
	biliRefreshTokenKey = "bili_ac_time_value"
	// biliCredentialCheckEvery 两次检查的间隔。B 站只在临近过期时才置 refresh=true，
	// 检查本身很轻（一个 GET），所以宁可勤一点：错过窗口就只能重新扫码。
	biliCredentialCheckEvery = 6 * time.Hour
)

// biliCredential 把库里的 Cookie 头 + refresh_token 还原成一条可续期的凭据。
//
// 兼容手填：手填只有 Cookie 头，refresh_token 为空，此时 Complete() 为假，
// 续期会直接报「只能重新扫码」，而不是发一个注定失败的请求。
func (s *Server) biliCredential() (bilibili.Credential, error) {
	var cred bilibili.Credential
	raw, err := s.st.GetSecret(s.box, "bili_cookie")
	if err != nil {
		return cred, err
	}
	cred = parseBiliCookieCredential(raw)
	if tok, err := s.st.GetSecret(s.box, biliRefreshTokenKey); err == nil {
		cred.RefreshToken = strings.TrimSpace(tok)
	}
	return cred, nil
}

// parseBiliCookieCredential 从一条 Cookie 头里拣出续期要用的四个字段。
//
// 只认这四个：别的（b_nut / buvid4 / _uuid …）发不发都不影响登录，留着反而让下次
// 「比对哪一段变了」更难。手填的 Cookie 一般没有 refresh_token，那一段由调用方另读。
func parseBiliCookieCredential(raw string) bilibili.Credential {
	var cred bilibili.Credential
	for _, seg := range strings.Split(raw, ";") {
		k, v, ok := strings.Cut(strings.TrimSpace(seg), "=")
		if !ok {
			continue
		}
		switch k {
		case "SESSDATA":
			cred.Sessdata = v
		case "bili_jct":
			cred.BiliJct = v
		case "DedeUserID":
			cred.DedeUserID = v
		case "buvid3":
			cred.Buvid3 = v
		}
	}
	return cred
}

// saveBiliCredential 把一条凭据写回库（扫码成功和续期成功都走它）。
func (s *Server) saveBiliCredential(cred bilibili.Credential) error {
	if err := s.st.SetSecret(s.box, "bili_cookie", cred.CookieHeader()); err != nil {
		return err
	}
	if cred.RefreshToken != "" {
		if err := s.st.SetSecret(s.box, biliRefreshTokenKey, cred.RefreshToken); err != nil {
			return err
		}
	}
	return nil
}

// handleBiliQrcodeGenerate 生成登录二维码（前端拿 url 画图、拿 key 轮询）。
func (s *Server) handleBiliQrcodeGenerate(w http.ResponseWriter, r *http.Request) {
	ad, ok := s.biliAdapter()
	if !ok {
		writeErr(w, http.StatusServiceUnavailable, "unavailable", "B 站适配器未就绪")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	qr, err := ad.GenerateQRCode(ctx)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "upstream", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, qr)
}

// handleBiliQrcodePoll 轮询扫码结果；成功时直接落库（前端不用回传任何凭据）。
func (s *Server) handleBiliQrcodePoll(w http.ResponseWriter, r *http.Request) {
	ad, ok := s.biliAdapter()
	if !ok {
		writeErr(w, http.StatusServiceUnavailable, "unavailable", "B 站适配器未就绪")
		return
	}
	key := strings.TrimSpace(r.URL.Query().Get("key"))
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	res, err := ad.PollQRCode(ctx, key)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "upstream", err.Error())
		return
	}
	out := map[string]any{"status": res.Status, "message": res.Message}
	if res.Status == bilibili.QRStatusSuccess && res.Credential != nil {
		if err := s.saveBiliCredential(*res.Credential); err != nil {
			writeErr(w, http.StatusInternalServerError, "internal", "保存登录态失败")
			return
		}
		s.log.Info("B 站扫码登录成功", "mid", res.Credential.DedeUserID,
			"可自动续期", res.Credential.RefreshToken != "")
		if name, err := s.biliLoginName(ctx); err == nil {
			out["uname"] = name
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// biliLoginName 顺手确认一下「这条新凭据到底登录上没有」。
func (s *Server) biliLoginName(ctx context.Context) (string, error) {
	ad, ok := s.biliAdapter()
	if !ok {
		return "", errNoAdapter
	}
	return ad.LoginName(ctx)
}

// CheckAndRefreshBiliCredential 检查一次并（必要时）续期，返回是否真的换了新凭据。
//
// 调用方：启动后的定时任务（见 cmd/myporn 的 runBiliCredentialScheduler）。
// 语义上「没续」不是错误——B 站只在临近过期时才要求续。
func (s *Server) CheckAndRefreshBiliCredential(ctx context.Context) (bool, error) {
	ad, ok := s.biliAdapter()
	if !ok {
		return false, errNoAdapter
	}
	cred, err := s.biliCredential()
	if err != nil {
		return false, err
	}
	if cred.Sessdata == "" {
		return false, nil // 没配 Cookie，什么都不用做
	}
	if cred.RefreshToken == "" {
		// 手填的 Cookie：问一下也该怪，不问也那样，直接留一行日志说明能力边界。
		s.log.Debug("B 站凭据没有 refresh_token，无法自动续期（需要扫码登录一次）")
		return false, nil
	}
	need, err := ad.NeedRefresh(ctx, cred)
	if err != nil {
		return false, err
	}
	if !need {
		return false, nil
	}
	fresh, err := ad.RefreshCredential(ctx, cred)
	if err != nil {
		return false, err
	}
	if err := s.saveBiliCredential(fresh); err != nil {
		return false, err
	}
	s.log.Info("B 站登录态已续期", "mid", fresh.DedeUserID)
	return true, nil
}
