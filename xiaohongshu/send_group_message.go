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

	groupMessageOverallTimeout = 180 * time.Second
	groupListWaitTimeout       = 20 * time.Second
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
func SendGroupMessage(ctx context.Context, page *rod.Page, in GroupMessageInput) (*GroupMessageResult, error) {
	if err := ValidateGroupMessageInput(in); err != nil {
		return nil, err
	}

	pp := page.Context(ctx).Timeout(groupMessageOverallTimeout)

	if err := openGroupIMPage(pp); err != nil {
		return nil, err
	}
	if err := ensureLoggedInSidebar(pp); err != nil {
		return nil, err
	}
	if err := waitConversationSidebar(pp); err != nil {
		return nil, err
	}
	if err := openGroupConversation(pp, in.GroupChat); err != nil {
		return nil, err
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
			return nil, err
		}
	}
	if txt := strings.TrimSpace(in.Text); txt != "" {
		if err := sendTextMessage(pp, txt); err != nil {
			return nil, err
		}
	}

	return &GroupMessageResult{
		GroupChat: in.GroupChat,
		DryRun:    false,
		Status:    "消息已发送并校验出现在会话中",
	}, nil
}

func openGroupIMPage(page *rod.Page) error {
	page.MustNavigate(groupIMEntryURL).MustWaitLoad()
	time.Sleep(afterNavigatePause)
	page.MustWaitStable()
	return nil
}

// ensureLoggedInSidebar 与登录检查一致：侧栏应出现用户入口。
func ensureLoggedInSidebar(page *rod.Page) error {
	ok, _, err := page.Has(`.main-container .user .link-wrapper .channel`)
	if err != nil {
		return errors.Wrap(err, "检测登录态失败")
	}
	if !ok {
		return errors.New("未登录：请先使用扫码登录后再发送群消息")
	}
	return nil
}

// waitConversationSidebar 等待左侧会话列表挂载；挂不到则视为当前页面不支持网页私信。
func waitConversationSidebar(page *rod.Page) error {
	deadline := time.Now().Add(groupListWaitTimeout)
	for time.Now().Before(deadline) {
		found, err := page.Eval(`() => {
			const body = document.body.innerText || '';
			if ((body.includes('暂不支持') && body.includes('私信')) || body.includes('请使用App') || body.includes('请使用 APP')) {
				return 'unsupported';
			}
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
						return 'ok';
					}
				}
			}
			return '';
		}`)
		if err != nil {
			return errors.Wrap(err, "检测会话区域失败")
		}
		switch found.Value.Str() {
		case "unsupported":
			return errors.New("当前网页端不支持私信/群聊或该账号未开放网页消息：无法发送")
		case "ok":
			return nil
		}
		time.Sleep(300 * time.Millisecond)
	}
	return errors.New("等待私信会话列表超时：网页端可能不支持群聊或未展示会话列表")
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
	// 精确匹配展示名（与创作平台 group_chat 一致），XPath 按规范拼接字面量。
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
	// 常见「发送」按钮文案
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
		// 部分交互是选中即发送，无单独按钮
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
	// XPath 1.0 concat 转义单引号
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
