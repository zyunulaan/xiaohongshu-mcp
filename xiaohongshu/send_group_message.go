package xiaohongshu

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-rod/rod"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
	"github.com/xpzouying/xiaohongshu-mcp/humanize"
)

const (
	groupIMEntryURL = "https://www.xiaohongshu.com/im"

	groupMessageOverallTimeout = 120 * time.Second
	imUILoadMaxWait            = 40 * time.Second
	sendVerifyTimeout          = 25 * time.Second

	navAttemptTimeout = 15 * time.Second // 单次导航超时
	waitLoadShort     = 8 * time.Second
	waitStableShort   = 2 * time.Second
	exploreNavTimeout = 22 * time.Second
)

// GroupMessageInput 发送群聊消息的参数（网页私信）。
type GroupMessageInput struct {
	GroupChat  string
	Text       string
	ImagePaths []string
	DryRun     bool
}

// GroupMessageResult 发送结果摘要。
type GroupMessageResult struct {
	GroupChat string `json:"group_chat"`
	DryRun    bool   `json:"dry_run"`
	Status    string `json:"status"`
}

// ValidateGroupMessageInput 校验参数并在启动浏览器前检查本地图片路径。
func ValidateGroupMessageInput(in GroupMessageInput) error {
	in.GroupChat = strings.TrimSpace(in.GroupChat)
	if in.GroupChat == "" {
		return errors.New("group_chat 不能为空")
	}
	if !in.DryRun {
		textEmpty := strings.TrimSpace(in.Text) == ""
		if textEmpty && len(in.ImagePaths) == 0 {
			return errors.New("至少需要提供 text 或 images 中的一项")
		}
	}
	for _, p := range in.ImagePaths {
		p = strings.TrimSpace(p)
		if p == "" {
			return errors.New("images 中存在空路径")
		}
		fi, err := os.Stat(p)
		if err != nil {
			return errors.Errorf("图片路径不存在或不可访问: %s: %v", p, err)
		}
		if fi.IsDir() {
			return errors.Errorf("图片路径指向目录，需要 jpg/png 文件: %s", p)
		}
		ext := strings.ToLower(filepath.Ext(p))
		switch ext {
		case ".jpg", ".jpeg", ".png":
		default:
			return errors.Errorf("仅支持 jpg/png 图片，当前扩展名 %q: %s", ext, p)
		}
	}
	return nil
}

// SendGroupMessage 打开私信页、按展示名精确匹配群聊并发送；dry_run 只打开并校验找到群聊。
func SendGroupMessage(ctx context.Context, page *rod.Page, in GroupMessageInput, debugArtifactsDir string) (res *GroupMessageResult, err error) {
	var exploreSnap sendGroupDebugPaths
	defer func() {
		if r := recover(); r != nil {
			err = errors.Errorf("send_group_message panic: %v", r)
			if debugArtifactsDir != "" && page != nil {
				paths, se := saveSendGroupMessageDebug(page, debugArtifactsDir)
				if se != nil {
					err = errors.Wrap(se, err.Error())
				} else {
					err = errors.Errorf("%v; panic 后页面调试: screenshot=%s html=%s url_file=%s", err, paths.png, paths.html, paths.urlFile)
				}
			}
			if exploreSnap.png != "" || exploreSnap.html != "" {
				err = errors.Errorf("%v; explore 快照: png=%s html=%s url_file=%s", err, exploreSnap.png, exploreSnap.html, exploreSnap.urlFile)
			}
			res = nil
		}
	}()

	if e := ValidateGroupMessageInput(in); e != nil {
		return nil, e
	}

	pp := page.Context(ctx).Timeout(groupMessageOverallTimeout)

	err = openGroupIMPage(ctx, pp, debugArtifactsDir, &exploreSnap)
	if err != nil {
		return nil, wrapWithSendGroupDebugExtra(pp, debugArtifactsDir, err, formatExplorePrefix(exploreSnap))
	}
	if err := waitConversationSidebar(pp); err != nil {
		return nil, wrapWithSendGroupDebugExtra(pp, debugArtifactsDir, err, formatExplorePrefix(exploreSnap))
	}
	if err := openGroupConversation(pp, in.GroupChat); err != nil {
		return nil, wrapWithSendGroupDebugExtra(pp, debugArtifactsDir, err, formatExplorePrefix(exploreSnap))
	}
	if in.DryRun {
		return &GroupMessageResult{
			GroupChat: in.GroupChat,
			DryRun:    true,
			Status:    "已找到群聊（dry_run，未发送）",
		}, nil
	}

	for _, path := range in.ImagePaths {
		path = strings.TrimSpace(path)
		if err := sendOneImageMessage(pp, path); err != nil {
			return nil, wrapWithSendGroupDebugExtra(pp, debugArtifactsDir, err, formatExplorePrefix(exploreSnap))
		}
	}
	if txt := strings.TrimSpace(in.Text); txt != "" {
		if err := sendTextMessage(pp, txt); err != nil {
			return nil, wrapWithSendGroupDebugExtra(pp, debugArtifactsDir, err, formatExplorePrefix(exploreSnap))
		}
	}

	return &GroupMessageResult{
		GroupChat: in.GroupChat,
		DryRun:    false,
		Status:    "消息已发送并校验出现在会话中",
	}, nil
}

func formatExplorePrefix(p sendGroupDebugPaths) string {
	if strings.TrimSpace(p.png) == "" && strings.TrimSpace(p.html) == "" {
		return ""
	}
	return fmt.Sprintf("explore 快照: png=%s html=%s url_file=%s", p.png, p.html, p.urlFile)
}

func softWaitLoad(page *rod.Page, d time.Duration) error {
	if page == nil {
		return nil
	}
	return page.Timeout(d).WaitLoad()
}

func softWaitStable(page *rod.Page, d time.Duration) error {
	if page == nil {
		return nil
	}
	return page.WaitStable(d)
}

func softNavigate(ctx context.Context, page *rod.Page, rawURL string) error {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return errors.New("empty URL")
	}
	navCtx, cancel := context.WithTimeout(ctx, navAttemptTimeout)
	defer cancel()
	p := page.Context(navCtx)
	return p.Navigate(rawURL)
}

// softOpenExplore 与 check_login_status 同址，但不使用 Must*，避免超时时 panic。
func softOpenExplore(ctx context.Context, page *rod.Page) error {
	navCtx, cancel := context.WithTimeout(ctx, exploreNavTimeout)
	defer cancel()
	p := page.Context(navCtx)
	if err := p.Navigate("https://www.xiaohongshu.com/explore"); err != nil {
		return errors.Wrap(err, "导航 explore")
	}
	if err := softWaitLoad(p, 12*time.Second); err != nil {
		logrus.Debugf("explore WaitLoad: %v", err)
	}
	if _, err := p.Timeout(8 * time.Second).Element(`div#app`); err != nil {
		logrus.Debugf("explore 未找到 div#app: %v", err)
	}
	return softWaitStable(p, waitStableShort)
}

// openGroupIMPage 先 soft explore + 侧栏登录检测，再保存 explore 快照（写入 *exploreSnapOut），依次尝试点击入口与多 URL。
func openGroupIMPage(ctx context.Context, page *rod.Page, debugDir string, exploreSnapOut *sendGroupDebugPaths) error {
	if err := softOpenExplore(ctx, page); err != nil {
		return errors.Wrap(err, "打开 explore 失败")
	}
	time.Sleep(600 * time.Millisecond)

	loginAction := NewLogin(page)
	isLoggedIn, err := loginAction.CheckLoggedInOnExploreLayout(ctx)
	if err != nil {
		return errors.Wrap(err, "检测登录态失败")
	}
	if !isLoggedIn {
		return errors.New("未登录：请先使用扫码登录后再发送群消息")
	}

	if debugDir != "" && exploreSnapOut != nil {
		snap, errS := saveSendGroupExploreSnapshot(page, debugDir)
		*exploreSnapOut = snap
		if errS != nil {
			logrus.Warnf("explore 快照未完整保存: %v", errS)
		}
	}

	var tried []string
	logTry := func(label, u string, navErr error) {
		tried = append(tried, fmt.Sprintf("%s %s -> %v", label, u, errStr(navErr)))
		logrus.Infof("send_group_message: try [%s] %s err=%v", label, u, navErr)
	}

	// 1) 侧栏「消息」「私信」点击
	if err := tryClickMessageEntry(page, ctx); err == nil {
		_ = softWaitStable(page, waitStableShort)
		time.Sleep(500 * time.Millisecond)
		if pageLooksLikeIM(page) {
			_ = softWaitStable(page, 1500*time.Millisecond)
			return nil
		}
		logTry("click_sidebar_message", "(in-page)", nil)
	} else {
		logrus.Debugf("未点到侧栏消息入口: %v", err)
	}

	// 2) 从 explore DOM 收集含 消息/私信/im 等关键词的站内链接
	pageLinks, _ := collectExploreMessageLinks(page)

	candidates := uniqueStringsOrdered(append(pageLinks,
		"https://www.xiaohongshu.com/notification",
		groupIMEntryURL,
	))

	for _, u := range candidates {
		navErr := softNavigate(ctx, page, u)
		logTry("navigate", u, navErr)
		_ = softWaitLoad(page, waitLoadShort)
		_ = softWaitStable(page, waitStableShort)
		time.Sleep(400 * time.Millisecond)
		if pageLooksLikeIM(page) {
			return nil
		}
		// 导航超时仍继续读当前 document
		if detectLoginWall(page) {
			return errors.Errorf("已进入登录墙页面（已尝试: %s）", summarizeTried(tried))
		}
	}

	return errors.Errorf("未能进入网页私信/IM（已尝试: %s）", summarizeTried(tried))
}

func errStr(e error) string {
	if e == nil {
		return "ok"
	}
	return e.Error()
}

func summarizeTried(tried []string) string {
	if len(tried) == 0 {
		return "(none)"
	}
	return strings.Join(tried, " | ")
}

func uniqueStringsOrdered(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

// collectExploreMessageLinks 从当前页收集可能是消息/私信/群聊/通知 的站内链接。
func collectExploreMessageLinks(page *rod.Page) ([]string, error) {
	res, err := page.Eval(`() => {
		const out = [];
		const seen = new Set();
		const keys = ['消息', '私信', '群聊', '通知', 'notification', 'chat', 'message', '/im', 'inbox'];
		for (const a of document.querySelectorAll('a[href]')) {
			const href = (a.getAttribute('href') || '').trim();
			if (!href || href === '#' || href.startsWith('javascript:')) continue;
			const text = (a.innerText || a.textContent || '').trim();
			let abs;
			try { abs = new URL(href, location.origin).href; } catch (e) { continue; }
			if (!abs.includes('xiaohongshu.com')) continue;
			const hay = (text + ' ' + href).toLowerCase();
			let hit = false;
			for (const k of keys) {
				if (hay.includes(String(k).toLowerCase())) { hit = true; break; }
			}
			if (!hit) continue;
			if (seen.has(abs)) continue;
			seen.add(abs);
			out.push(abs);
		}
		return JSON.stringify(out);
	}`)
	if err != nil {
		return nil, err
	}
	raw := res.Value.String()
	var list []string
	if raw == "" {
		return nil, nil
	}
	if uerr := json.Unmarshal([]byte(raw), &list); uerr != nil {
		return nil, uerr
	}
	return list, nil
}

func tryClickMessageEntry(page *rod.Page, ctx context.Context) error {
	res, err := page.Eval(`() => {
		const labels = ['消息', '私信'];
		const cand = Array.from(document.querySelectorAll('a, [role="link"], span, div'));
		for (const el of cand) {
			const t = (el.innerText || el.textContent || '').trim();
			if (!labels.includes(t)) continue;
			let n = el;
			for (let i = 0; i < 8 && n; i++) {
				if (n.tagName === 'A' || (n.getAttribute && n.getAttribute('role') === 'link')) {
					n.click();
					return true;
				}
				n = n.parentElement;
			}
			el.click();
			return true;
		}
		return false;
	}`)
	if err != nil {
		return err
	}
	if !res.Value.Bool() {
		return errors.New("未找到消息入口")
	}
	humanize.Delay(ctx, humanize.AfterClick)
	return nil
}

func pageLooksLikeIM(page *rod.Page) bool {
	res, err := page.Eval(`() => {
		const href = location.href || '';
		if (/\/im(\/|\?|#|$)/i.test(href)) return 'im';
		const qs = [
			'[class*="conversation"]',
			'[class*="session"]',
			'[class*="session-list"]',
			'[class*="chat-list"]',
			'[class*="msg-list"]',
			'.im-session-list',
			'[class*="im-list"]'
		];
		for (const s of qs) {
			const els = document.querySelectorAll(s);
			for (const el of els) {
				const r = el.getBoundingClientRect();
				if (r.width > 80 && r.height > 120 && el.querySelectorAll('div, li').length >= 2) {
					return 'list';
				}
			}
		}
		return '';
	}`)
	if err != nil {
		return false
	}
	switch res.Value.Str() {
	case "im", "list":
		return true
	default:
		return false
	}
}

func detectLoginWall(page *rod.Page) bool {
	res, err := page.Eval(`() => !!document.querySelector('.login-container .qrcode-img') || !!document.querySelector('.login-container')`)
	if err != nil {
		return false
	}
	return res.Value.Bool()
}

func detectUnsupportedIM(page *rod.Page) bool {
	res, err := page.Eval(`() => {
		const body = document.body.innerText || '';
		if ((body.includes('暂不支持') && body.includes('私信')) || body.includes('请使用App') || body.includes('请使用 APP')) {
			return true;
		}
		return false;
	}`)
	return err == nil && res.Value.Bool()
}

func waitConversationSidebar(page *rod.Page) error {
	deadline := time.Now().Add(imUILoadMaxWait)
	for time.Now().Before(deadline) {
		if detectLoginWall(page) {
			return errors.New("当前页面展示登录界面，会话可能未生效：请先扫码登录或检查 cookies")
		}
		if detectUnsupportedIM(page) {
			return errors.New("当前网页端不支持私信/群聊或该账号未开放网页消息：无法发送")
		}
		if pageLooksLikeIM(page) {
			return nil
		}
		time.Sleep(300 * time.Millisecond)
	}
	return errors.New("40s 内未检测到网页私信/IM 界面（非 explore 首页布局），可能路由未进入 /im")
}

func openGroupConversation(page *rod.Page, groupName string) error {
	groupName = strings.TrimSpace(groupName)
	if err := clickConversationByExactTitle(page, groupName); err != nil {
		return err
	}
	if err := waitActiveConversationTitle(page, groupName); err != nil {
		return errors.Wrap(err, "打开会话后校验群名失败")
	}
	time.Sleep(500 * time.Millisecond)
	return nil
}

func clickConversationByExactTitle(page *rod.Page, title string) error {
	want := strings.TrimSpace(title)
	xpath := fmt.Sprintf(`//*[normalize-space(string(.))=%s]`, xpathStringLiteral(want))
	el, err := page.Timeout(25 * time.Second).Element("xpath=" + xpath)
	if err != nil {
		return errors.Wrap(formatGroupChatNotFoundError(want, nil), "未在会话列表中找到该群聊")
	}
	row := el
	for i := 0; i < 8; i++ {
		if row == nil {
			break
		}
		tagJSON, errTag := row.Property("tagName")
		if errTag == nil && strings.EqualFold(tagJSON.Str(), "li") {
			break
		}
		parent, err := row.Parent()
		if err != nil {
			break
		}
		row = parent
	}
	humanize.Delay(page.GetContext(), humanize.BeforeClick)
	clickTarget := row
	if clickTarget == nil {
		clickTarget = el
	}
	if err := humanize.Click(clickTarget); err != nil {
		if err2 := humanize.Click(el); err2 != nil {
			return errors.Wrap(err, "点击群聊会话失败")
		}
	}
	humanize.Delay(page.GetContext(), humanize.AfterClick)
	_ = softWaitStable(page, waitStableShort)
	return nil
}

func waitActiveConversationTitle(page *rod.Page, title string) error {
	deadline := time.Now().Add(15 * time.Second)
	want := strings.TrimSpace(title)
	for time.Now().Before(deadline) {
		res, err := page.Eval(`() => document.body.innerText || ''`)
		if err == nil {
			body := res.Value.Str()
			lines := strings.Split(body, "\n")
			for _, line := range lines {
				if groupChatNameMatches(strings.TrimSpace(line), want) {
					return nil
				}
			}
		}
		time.Sleep(250 * time.Millisecond)
	}
	return errors.Errorf("未在详情区看到群聊名称 %q（可能未打开或网页结构已变）", want)
}

func findChatImageFileInput(page *rod.Page) (*rod.Element, error) {
	inputs, err := page.Elements(`input[type=file]`)
	if err != nil {
		return nil, err
	}
	for _, inp := range inputs {
		acc, _ := inp.Attribute("accept")
		if acc != nil && strings.Contains(*acc, "image") {
			return inp, nil
		}
	}
	for _, inp := range inputs {
		v, _ := inp.Visible()
		if v {
			return inp, nil
		}
	}
	return nil, errors.New("未找到图片上传 input")
}

func findChatTextInput(page *rod.Page) (*rod.Element, error) {
	if ta, err := page.Element("textarea"); err == nil && ta != nil {
		return ta, nil
	}
	return page.Element(`[contenteditable="true"]`)
}

func findSendButton(page *rod.Page) (*rod.Element, error) {
	candidates := []string{
		`//button[contains(normalize-space(.),"发送")]`,
		`//div[@role="button"][contains(normalize-space(.),"发送")]`,
		`//*[self::button or self::div][contains(@class,"send")]`,
	}
	for _, xp := range candidates {
		if el, err := page.Timeout(5 * time.Second).Element(`xpath=` + xp); err == nil && el != nil {
			if vis, _ := el.Visible(); vis {
				return el, nil
			}
		}
	}
	return nil, errors.New("未找到发送按钮")
}

func sendOneImageMessage(page *rod.Page, path string) error {
	inp, err := findChatImageFileInput(page)
	if err != nil {
		return errors.Wrap(err, "群聊不支持图片上传或当前页面无图片入口")
	}
	before, err := countMessagePaneImages(page)
	if err != nil {
		return err
	}
	if err := inp.SetFiles([]string{path}); err != nil {
		return errors.Wrap(err, "上传图片失败")
	}
	time.Sleep(800 * time.Millisecond)
	if err := clickSendIfNeededAfterUpload(page); err != nil {
		return err
	}
	if err := waitImageMessageRendered(page, before); err != nil {
		return errors.Wrap(err, "发送图片后未在会话中确认到图片消息")
	}
	return nil
}

func clickSendIfNeededAfterUpload(page *rod.Page) error {
	btn, err := findSendButton(page)
	if err != nil {
		logrus.Infof("发送图片：未找到发送按钮，假设选中即发送: %v", err)
		return nil
	}
	humanize.Delay(page.GetContext(), humanize.BeforeClick)
	if err := humanize.Click(btn); err != nil {
		return errors.Wrap(err, "点击发送图片失败")
	}
	humanize.Delay(page.GetContext(), humanize.AfterClick)
	_ = softWaitStable(page, waitStableShort)
	return nil
}

func sendTextMessage(page *rod.Page, text string) error {
	ti, err := findChatTextInput(page)
	if err != nil {
		return errors.Wrap(err, "未找到文本输入框")
	}
	if err := humanize.Type(page.GetContext(), ti, text); err != nil {
		return errors.Wrap(err, "输入文字失败")
	}
	btn, err := findSendButton(page)
	if err != nil {
		return errors.Wrap(err, "无法发送文字：未找到发送按钮")
	}
	humanize.Delay(page.GetContext(), humanize.BeforeClick)
	if err := humanize.Click(btn); err != nil {
		return errors.Wrap(err, "点击发送文字失败")
	}
	humanize.Delay(page.GetContext(), humanize.AfterClick)
	_ = softWaitStable(page, waitStableShort)
	if err := waitMessageAppears(page, text); err != nil {
		return errors.Wrap(err, "发送文字后未在会话中确认到消息")
	}
	return nil
}

func waitMessageAppears(page *rod.Page, marker string) error {
	marker = strings.TrimSpace(marker)
	if marker == "" {
		return errors.New("消息内容为空")
	}
	deadline := time.Now().Add(sendVerifyTimeout)
	for time.Now().Before(deadline) {
		res, err := page.Eval(`() => document.body.innerText || ''`)
		if err == nil {
			body := res.Value.Str()
			if strings.Contains(body, marker) {
				return nil
			}
		}
		time.Sleep(400 * time.Millisecond)
	}
	return errors.Errorf("超时：会话中未出现文字 %q", marker)
}

func countMessagePaneImages(page *rod.Page) (int, error) {
	res, err := page.Eval(`() => {
		const pane = document.querySelector('[class*="message"]') ||
			document.querySelector('[class*="msg"]') ||
			document.querySelector('main') ||
			document.body;
		return pane.querySelectorAll('img').length;
	}`)
	if err != nil {
		return 0, errors.Wrap(err, "统计会话区图片数量失败")
	}
	return int(res.Value.Num()), nil
}

func waitImageMessageRendered(page *rod.Page, beforeCount int) error {
	deadline := time.Now().Add(sendVerifyTimeout)
	for time.Now().Before(deadline) {
		n, err := countMessagePaneImages(page)
		if err == nil && n > beforeCount {
			return nil
		}
		time.Sleep(400 * time.Millisecond)
	}
	return errors.New("超时：会话区 <img> 数量未增加（图片可能未发出或页面结构已变）")
}

func xpathStringLiteral(s string) string {
	if !strings.Contains(s, "'") {
		return "'" + s + "'"
	}
	if !strings.Contains(s, `"`) {
		return `"` + s + `"`
	}
	var b strings.Builder
	parts := strings.Split(s, "'")
	for i, p := range parts {
		if i > 0 {
			b.WriteString(`,"'",`)
		}
		b.WriteString("'")
		b.WriteString(p)
		b.WriteString("'")
	}
	return "concat(" + b.String() + ")"
}
