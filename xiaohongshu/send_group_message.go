package xiaohongshu

import (
	"context"
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
	// groupIMEntryURL 网页私信入口（群聊与单聊均在此路由下，依赖账号与产品灰度）。
	groupIMEntryURL = "https://www.xiaohongshu.com/im"

	groupMessageOverallTimeout = 90 * time.Second
	imUILoadMaxWait            = 40 * time.Second // 非 IM 界面尽快失败（~40s）
	afterNavigatePause         = 1200 * time.Millisecond
	sendVerifyTimeout          = 25 * time.Second
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
// dry_run 时允许仅有群名；非 dry_run 时 text 与 images 至少一项非空。
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
// debugArtifactsDir 非空时，失败会在该目录写入截图/HTML/URL 供排查。
func SendGroupMessage(ctx context.Context, page *rod.Page, in GroupMessageInput, debugArtifactsDir string) (*GroupMessageResult, error) {
	if err := ValidateGroupMessageInput(in); err != nil {
		return nil, err
	}

	pp := page.Context(ctx).Timeout(groupMessageOverallTimeout)

	if err := openGroupIMPage(ctx, pp); err != nil {
		return nil, wrapWithSendGroupDebug(pp, debugArtifactsDir, err)
	}
	if err := waitConversationSidebar(pp); err != nil {
		return nil, wrapWithSendGroupDebug(pp, debugArtifactsDir, err)
	}
	if err := openGroupConversation(pp, in.GroupChat); err != nil {
		return nil, wrapWithSendGroupDebug(pp, debugArtifactsDir, err)
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
			return nil, wrapWithSendGroupDebug(pp, debugArtifactsDir, err)
		}
	}
	if txt := strings.TrimSpace(in.Text); txt != "" {
		if err := sendTextMessage(pp, txt); err != nil {
			return nil, wrapWithSendGroupDebug(pp, debugArtifactsDir, err)
		}
	}

	return &GroupMessageResult{
		GroupChat: in.GroupChat,
		DryRun:    false,
		Status:    "消息已发送并校验出现在会话中",
	}, nil
}

// openGroupIMPage 与 check_login_status 一致：先落地 explore 注入登录态，再用 LoginAction 检测；再尝试点「消息/私信」或直达 /im。
func openGroupIMPage(ctx context.Context, page *rod.Page) error {
	pp := page.Context(ctx).Timeout(60 * time.Second)

	if err := NewNavigate(pp).ToExplorePage(ctx); err != nil {
		return errors.Wrap(err, "打开 explore 失败")
	}
	time.Sleep(1000 * time.Millisecond)

	loginAction := NewLogin(pp)
	isLoggedIn, err := loginAction.CheckLoginStatus(ctx)
	if err != nil {
		return errors.Wrap(err, "检测登录态失败")
	}
	if !isLoggedIn {
		return errors.New("未登录：请先使用扫码登录后再发送群消息")
	}

	// 尝试从侧栏进私信（单页应用内导航，cookie 与首页一致）
	if err := tryClickMessageEntry(pp, ctx); err != nil {
		logrus.Debugf("侧栏未点到消息入口，改直达 /im: %v", err)
	}
	if pageLooksLikeIM(pp) {
		pp.MustWaitStable()
		return nil
	}

	pp.Timeout(45 * time.Second).MustNavigate(groupIMEntryURL).MustWaitLoad()
	time.Sleep(afterNavigatePause)
	pp.MustWaitStable()
	return nil
}

// tryClickMessageEntry 点击侧栏或顶栏文案为「消息」「私信」的入口。
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

// pageLooksLikeIM 当前是否已处于私信/IM 壳（URL 或会话列表区域）。
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

// detectLoginWall explore / im 上若出现扫码登录容器，视为未带入会话。
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

// waitConversationSidebar 等待 IM UI；40s 内既不是 IM 又不像登录墙则失败。
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
	page.MustWaitStable()
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
	page.MustWaitStable()
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
	page.MustWaitStable()
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
