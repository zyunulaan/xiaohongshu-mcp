package xiaohongshu

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/input"
	"github.com/go-rod/rod/lib/proto"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
	"github.com/xpzouying/xiaohongshu-mcp/humanize"
)

const (
	groupChatEntryURL = "https://www.xiaohongshu.com/chat?channel_id=&channel_type=explore_feed"

	groupMessageOverallTimeout = 120 * time.Second
	conversationListMaxWait    = 25 * time.Second // /chat 会话列表就绪
	sendVerifyTimeout          = 25 * time.Second
	composerFindTimeout        = 20 * time.Second
	stepOperationTimeout       = 45 * time.Second
	navAttemptTimeout          = 15 * time.Second
	waitLoadShort              = 10 * time.Second
	waitStableShort            = 2 * time.Second
	exploreNavTimeout          = 22 * time.Second
	maxLeftPanelScrollForGroup = 14
	leftPanelScrollStepPx      = 220
)

// GroupMessageInput 发送群聊消息的参数（网页私信 /chat）。
type GroupMessageInput struct {
	GroupChat  string
	Text       string
	ImagePaths []string
	DryRun     bool
}

// GroupMessageSnapshot 调试快照路径（_chat / _explore 等）。
type GroupMessageSnapshot struct {
	PNG     string `json:"png,omitempty"`
	HTML    string `json:"html,omitempty"`
	URLFile string `json:"url_file,omitempty"`
}

// GroupDryRunDiagnostics dry_run 时匹配的 DOM 信息。
type GroupDryRunDiagnostics struct {
	MatchedNameText string `json:"matched_name_text,omitempty"`
	MatchedSelector string `json:"matched_selector,omitempty"`
	InputVisible    bool   `json:"input_visible"`
	ScrollAttempts  int    `json:"scroll_attempts"`
	ChatURL         string `json:"chat_url,omitempty"`
}

// GroupMessageResult 发送结果摘要。
type GroupMessageResult struct {
	GroupChat            string                  `json:"group_chat"`
	DryRun               bool                    `json:"dry_run"`
	Status               string                  `json:"status"`
	ChatSnapshot         *GroupMessageSnapshot   `json:"chat_snapshot,omitempty"`
	ConversationSnapshot *GroupMessageSnapshot   `json:"conversation_snapshot,omitempty"`
	DryRunDiagnostics    *GroupDryRunDiagnostics `json:"dry_run_diagnostics,omitempty"`
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

// SendGroupMessage 打开 /chat、在左侧列表匹配群聊并发送；dry_run 只校验并返回诊断信息。
func SendGroupMessage(ctx context.Context, page *rod.Page, in GroupMessageInput, debugArtifactsDir string) (res *GroupMessageResult, err error) {
	var exploreSnap sendGroupDebugPaths
	var chatSnap sendGroupDebugPaths
	var convSnap sendGroupDebugPaths
	defer func() {
		if r := recover(); r != nil {
			err = errors.Errorf("send_group_message panic: %v", r)
			if debugArtifactsDir != "" && page != nil {
				paths, se := saveSendGroupMessageDebugFresh(page, debugArtifactsDir)
				if se != nil {
					err = errors.Wrap(se, err.Error())
				} else {
					err = errors.Errorf("%v; panic 后页面调试: screenshot=%s html=%s url_file=%s", err, paths.png, paths.html, paths.urlFile)
				}
			}
			if exploreSnap.png != "" || exploreSnap.html != "" {
				err = errors.Errorf("%v; explore 快照: png=%s html=%s url_file=%s", err, exploreSnap.png, exploreSnap.html, exploreSnap.urlFile)
			}
			if chatSnap.png != "" || chatSnap.html != "" {
				err = errors.Errorf("%v; chat 快照: png=%s html=%s url_file=%s", err, chatSnap.png, chatSnap.html, chatSnap.urlFile)
			}
			if convSnap.png != "" || convSnap.html != "" {
				err = errors.Errorf("%v; 会话快照: png=%s html=%s url_file=%s", err, convSnap.png, convSnap.html, convSnap.urlFile)
			}
			res = nil
		}
	}()

	if e := ValidateGroupMessageInput(in); e != nil {
		return nil, e
	}

	pp := page.Context(context.Background()).Timeout(groupMessageOverallTimeout)

	err = openChatPage(pp, debugArtifactsDir, &exploreSnap, &chatSnap)
	if err != nil {
		return nil, wrapWithSendGroupDebugExtra(pp, debugArtifactsDir, err, formatExploreChatPrefix(exploreSnap, chatSnap, sendGroupDebugPaths{}))
	}

	diag, convSnap, err := findAndOpenGroupConversation(pp, in.GroupChat, in.DryRun, debugArtifactsDir)
	if err != nil {
		return nil, wrapWithSendGroupDebugExtra(pp, debugArtifactsDir, err, formatExploreChatPrefix(exploreSnap, chatSnap, convSnap))
	}

	snapPtr := debugPathsToSnapshot(chatSnap)
	convPtr := debugPathsToSnapshot(convSnap)

	if in.DryRun {
		out := &GroupMessageResult{
			GroupChat:            in.GroupChat,
			DryRun:               true,
			Status:               "dry_run：已在 /chat 打开群聊且输入框可见（未发送）",
			ChatSnapshot:         snapPtr,
			ConversationSnapshot: convPtr,
			DryRunDiagnostics:    diag,
		}
		if diag != nil && !diag.InputVisible {
			out.Status = "dry_run：已点击群聊但未检测到输入框"
		}
		return out, nil
	}

	for _, path := range in.ImagePaths {
		path = strings.TrimSpace(path)
		if err := sendOneImageMessage(pp, path, debugArtifactsDir); err != nil {
			return nil, wrapWithSendGroupDebugExtra(pp, debugArtifactsDir, err, formatExploreChatPrefix(exploreSnap, chatSnap, convSnap))
		}
	}
	if txt := strings.TrimSpace(in.Text); txt != "" {
		if err := sendTextMessage(pp, txt); err != nil {
			return nil, wrapWithSendGroupDebugExtra(pp, debugArtifactsDir, err, formatExploreChatPrefix(exploreSnap, chatSnap, convSnap))
		}
	}

	return &GroupMessageResult{
		GroupChat:            in.GroupChat,
		DryRun:               false,
		Status:               "消息已发送并校验出现在会话中",
		ChatSnapshot:         snapPtr,
		ConversationSnapshot: convPtr,
	}, nil
}

func debugPathsToSnapshot(p sendGroupDebugPaths) *GroupMessageSnapshot {
	if strings.TrimSpace(p.png) == "" && strings.TrimSpace(p.html) == "" {
		return nil
	}
	return &GroupMessageSnapshot{PNG: p.png, HTML: p.html, URLFile: p.urlFile}
}

func formatExploreChatPrefix(explore, chat, conv sendGroupDebugPaths) string {
	var parts []string
	if strings.TrimSpace(explore.png) != "" || strings.TrimSpace(explore.html) != "" {
		parts = append(parts, fmt.Sprintf("explore 快照: png=%s html=%s url_file=%s", explore.png, explore.html, explore.urlFile))
	}
	if strings.TrimSpace(chat.png) != "" || strings.TrimSpace(chat.html) != "" {
		parts = append(parts, fmt.Sprintf("chat 快照: png=%s html=%s url_file=%s", chat.png, chat.html, chat.urlFile))
	}
	if strings.TrimSpace(conv.png) != "" || strings.TrimSpace(conv.html) != "" {
		parts = append(parts, fmt.Sprintf("会话快照: png=%s html=%s url_file=%s", conv.png, conv.html, conv.urlFile))
	}
	return strings.Join(parts, "; ")
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

func softNavigate(page *rod.Page, rawURL string) error {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return errors.New("empty URL")
	}
	navCtx, cancel := context.WithTimeout(context.Background(), navAttemptTimeout)
	defer cancel()
	p := page.Context(navCtx)
	return p.Navigate(rawURL)
}

func softOpenExplore(page *rod.Page) error {
	navCtx, cancel := context.WithTimeout(context.Background(), exploreNavTimeout)
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

// openChatPage explore → 登录检测 → explore 快照 → 点侧栏「消息」或直达 /chat → 保存 _chat 快照。
func openChatPage(page *rod.Page, debugDir string, exploreOut, chatOut *sendGroupDebugPaths) error {
	if err := softOpenExplore(page); err != nil {
		return errors.Wrap(err, "打开 explore 失败")
	}
	time.Sleep(500 * time.Millisecond)

	loginAction := NewLogin(page)
	isLoggedIn, err := loginAction.CheckLoggedInOnExploreLayout(context.Background())
	if err != nil {
		return errors.Wrap(err, "检测登录态失败")
	}
	if !isLoggedIn {
		return errors.New("未登录：请先使用扫码登录后再发送群消息")
	}

	if debugDir != "" && exploreOut != nil {
		snap, errS := saveSendGroupExploreSnapshot(page, debugDir)
		*exploreOut = snap
		if errS != nil {
			logrus.Warnf("explore 快照未完整保存: %v", errS)
		}
	}

	clicked := tryClickSidebarMessageLink(page)
	logrus.Infof("send_group_message: sidebar 消息链接点击: %v", clicked)
	if !clicked {
		logrus.Infof("send_group_message: 直达 %s", groupChatEntryURL)
		if err := softNavigate(page, groupChatEntryURL); err != nil {
			logrus.Infof("send_group_message: navigate /chat err=%v（仍尝试读当前页）", err)
		}
	} else {
		_ = softWaitLoad(page, waitLoadShort)
		_ = softWaitStable(page, waitStableShort)
		time.Sleep(600 * time.Millisecond)
		href := pageURLString(page)
		if !strings.Contains(strings.ToLower(href), "/chat") {
			logrus.Infof("send_group_message: 点击后 URL 不含 /chat，再导航 %s 当前=%q", groupChatEntryURL, href)
			if err := softNavigate(page, groupChatEntryURL); err != nil {
				logrus.Infof("send_group_message: 二次 navigate /chat err=%v", err)
			}
		}
	}

	_ = softWaitLoad(page, waitLoadShort)
	_ = softWaitStable(page, waitStableShort)
	time.Sleep(400 * time.Millisecond)

	if debugDir != "" && chatOut != nil {
		snap, errS := saveSendGroupChatSnapshot(page, debugDir)
		*chatOut = snap
		if errS != nil {
			logrus.Warnf("chat 快照未完整保存: %v", errS)
		}
	}

	if detectLoginWall(page) {
		return errors.New("当前页面展示登录界面，会话可能未生效")
	}
	if detectUnsupportedIM(page) {
		return errors.New("当前网页端不支持私信/群聊或该账号未开放网页消息：无法发送")
	}

	if err := waitChatConversationList(page); err != nil {
		href := pageURLString(page)
		return errors.Wrapf(err, "当前 URL=%q", href)
	}
	return nil
}

func pageURLString(page *rod.Page) string {
	info, err := page.Info()
	if err != nil || info == nil {
		return ""
	}
	return strings.TrimSpace(info.URL)
}

// tryClickSidebarMessageLink 点击侧栏 #im_chat_tab 或 span.channel「消息」所在链接。
func tryClickSidebarMessageLink(page *rod.Page) bool {
	res, err := page.Eval(`() => {
		const byId = document.querySelector('#im_chat_tab');
		if (byId) {
			let n = byId;
			for (let i = 0; i < 12 && n; i++) {
				if (n.tagName === 'A' && n.getAttribute('href')) {
					n.click();
					return true;
				}
				n = n.parentElement;
			}
			byId.click();
			return true;
		}
		const spans = Array.from(document.querySelectorAll('span.channel'));
		for (const sp of spans) {
			const t = (sp.innerText || sp.textContent || '').trim();
			if (t !== '消息' && t !== '私信') continue;
			let n = sp;
			for (let i = 0; i < 12 && n; i++) {
				if (n.tagName === 'A' && n.getAttribute('href')) {
					n.click();
					return true;
				}
				n = n.parentElement;
			}
		}
		return false;
	}`)
	if err != nil {
		logrus.Debugf("tryClickSidebarMessageLink eval: %v", err)
		return false
	}
	if !res.Value.Bool() {
		return false
	}
	humanize.Delay(context.Background(), humanize.AfterClick)
	return true
}

func pageLooksLikeChat(page *rod.Page) bool {
	res, err := page.Eval(`() => {
		const href = (location.href || '');
		if (/\/chat(\/|\?|#|$)/i.test(href)) return true;
		const leftSelectors = [
			'[class*="session"]', '[class*="conversation"]', '[class*="chat-list"]',
			'[class*="msg-list"]', '[class*="im-list"]', 'aside', 'nav'
		];
		let best = null;
		for (const sel of leftSelectors) {
			for (const el of document.querySelectorAll(sel)) {
				const r = el.getBoundingClientRect();
				if (r.width < 60 || r.height < 100) continue;
				if (r.left > window.innerWidth * 0.42) continue;
				const rows = el.querySelectorAll('div, li, [role="listitem"], [role="button"]');
				if (rows.length >= 2) {
					if (!best || rows.length > best.cnt) {
						best = { cnt: rows.length, w: r.width };
					}
				}
			}
		}
		return best !== null && best.cnt >= 2;
	}`)
	if err != nil {
		return false
	}
	return res.Value.Bool()
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

func waitChatConversationList(page *rod.Page) error {
	deadline := time.Now().Add(conversationListMaxWait)
	for time.Now().Before(deadline) {
		if pageLooksLikeChat(page) {
			return nil
		}
		time.Sleep(300 * time.Millisecond)
	}
	return errors.New("约 25s 内未在 /chat 检测到左侧会话列表布局")
}

// findAndOpenGroupConversation 左侧栏滚动查找群名（大小写不敏感），点击后校验输入框并保存 *_conv 快照。
func findAndOpenGroupConversation(page *rod.Page, groupName string, dryRun bool, debugDir string) (*GroupDryRunDiagnostics, sendGroupDebugPaths, error) {
	want := strings.TrimSpace(groupName)
	if want == "" {
		return nil, sendGroupDebugPaths{}, errors.New("group_chat 为空")
	}

	var lastDiag *GroupDryRunDiagnostics
	for attempt := 0; attempt <= maxLeftPanelScrollForGroup; attempt++ {
		res, err := page.Eval(`(want) => {
			const norm = (s) => String(s || '').trim();
			const cmp = (a, b) => norm(a).toLowerCase() === norm(b).toLowerCase();
			const visible = (el) => {
				if (!el) return false;
				const st = window.getComputedStyle(el);
				if (st.display === 'none' || st.visibility === 'hidden' || parseFloat(st.opacity || '1') < 0.05) return false;
				const r = el.getBoundingClientRect();
				return r.width > 2 && r.height > 2 && r.bottom > 0 && r.right > 0;
			};
			const leftPanels = [];
			const sels = ['aside', 'nav', '[class*="session"]', '[class*="conversation"]', '[class*="chat-list"]', '[class*="msg-list"]', '[class*="im-list"]', '[class*="list"]'];
			for (const sel of sels) {
				for (const el of document.querySelectorAll(sel)) {
					const r = el.getBoundingClientRect();
					if (r.width < 80 || r.height < 120) continue;
					if (r.left > window.innerWidth * 0.45) continue;
					leftPanels.push(el);
				}
			}
			let root = leftPanels.reduce((a, b) => {
				if (!a) return b;
				const ra = a.getBoundingClientRect();
				const rb = b.getBoundingClientRect();
				return (a.querySelectorAll('div, li').length >= b.querySelectorAll('div, li').length) ? a : b;
			}, null);
			if (!root) root = document.body;

			const cand = root.querySelectorAll('*');
			for (const el of cand) {
				if (!visible(el)) continue;
				const t = (el.innerText || el.textContent || '').trim();
				if (!t || t.length > 80) continue;
				const lines = t.split(/\n/).map(x => x.trim()).filter(Boolean);
				for (const line of lines) {
					if (!cmp(line, want)) continue;
					const tag = (el.tagName || '').toLowerCase();
					let path = tag;
					if (el.id) path += '#' + el.id;
					if (el.className && typeof el.className === 'string') {
						const c = el.className.trim().split(/\s+/).slice(0, 2).join('.');
						if (c) path += '.' + c;
					}
					let clickEl = el;
					for (let i = 0; i < 6 && clickEl; i++) {
						const role = clickEl.getAttribute && clickEl.getAttribute('role');
						if (clickEl.tagName === 'LI' || role === 'button' || role === 'option' || role === 'listitem') {
							break;
						}
						clickEl = clickEl.parentElement;
					}
					return JSON.stringify({ found: true, text: line, selector: path, tag: tag });
				}
			}
			return JSON.stringify({ found: false });
		}`, want)
		if err != nil {
			return nil, sendGroupDebugPaths{}, errors.Wrap(err, "左侧栏查找群名脚本失败")
		}

		var parsed struct {
			Found    bool   `json:"found"`
			Text     string `json:"text"`
			Selector string `json:"selector"`
			Tag      string `json:"tag"`
		}
		if uerr := json.Unmarshal([]byte(res.Value.String()), &parsed); uerr != nil {
			return nil, sendGroupDebugPaths{}, uerr
		}

		if !parsed.Found {
			scrollLeftPanelDown(page)
			time.Sleep(220 * time.Millisecond)
			lastDiag = &GroupDryRunDiagnostics{ScrollAttempts: attempt + 1, ChatURL: pageURLString(page)}
			continue
		}

		clickRes, err := page.Eval(`(want) => {
			const norm = (s) => String(s || '').trim();
			const cmp = (a, b) => norm(a).toLowerCase() === norm(b).toLowerCase();
			const visible = (el) => {
				if (!el) return false;
				const st = window.getComputedStyle(el);
				if (st.display === 'none' || st.visibility === 'hidden') return false;
				const r = el.getBoundingClientRect();
				return r.width > 2 && r.height > 2;
			};
			const roots = document.querySelectorAll('aside, nav, [class*="session"], [class*="conversation"], [class*="chat-list"], [class*="msg-list"]');
			let root = null;
			let best = 0;
			for (const el of roots) {
				const r = el.getBoundingClientRect();
				if (r.left > window.innerWidth * 0.45) continue;
				const n = el.querySelectorAll('div, li').length;
				if (n > best) { best = n; root = el; }
			}
			if (!root) root = document.body;
			for (const el of root.querySelectorAll('*')) {
				if (!visible(el)) continue;
				const t = (el.innerText || el.textContent || '').trim();
				if (!t || t.length > 80) continue;
				for (const line of t.split(/\n/).map(x => x.trim()).filter(Boolean)) {
					if (!cmp(line, want)) continue;
					let clickEl = el;
					for (let i = 0; i < 8 && clickEl; i++) {
						const role = clickEl.getAttribute && clickEl.getAttribute('role');
						if (clickEl.tagName === 'LI' || role === 'button' || role === 'option') {
							clickEl.click();
							return true;
						}
						clickEl = clickEl.parentElement;
					}
					el.click();
					return true;
				}
			}
			return false;
		}`, want)
		if err != nil {
			return nil, sendGroupDebugPaths{}, errors.Wrap(err, "点击群聊项失败")
		}
		if !clickRes.Value.Bool() {
			scrollLeftPanelDown(page)
			continue
		}

		_ = softWaitStable(page, waitStableShort)
		time.Sleep(400 * time.Millisecond)

		var convPaths sendGroupDebugPaths
		if strings.TrimSpace(debugDir) != "" {
			snap, errS := saveSendGroupConvSnapshot(page, debugDir)
			convPaths = snap
			if errS != nil {
				logrus.Warnf("会话 composer 快照未完整保存: %v", errS)
			}
		}

		inputVis := false
		composerWaitCtx, composerWaitCancel := context.WithTimeout(context.Background(), composerFindTimeout)
		pollPage := page.Context(composerWaitCtx).Timeout(composerFindTimeout)
		for composerWaitCtx.Err() == nil {
			v, errV := messageComposerVisible(pollPage)
			if errV != nil {
				logrus.Debugf("message composer poll: %v", errV)
			} else if v {
				inputVis = true
				break
			}
			time.Sleep(200 * time.Millisecond)
		}
		composerWaitCancel()
		diag := &GroupDryRunDiagnostics{
			MatchedNameText: parsed.Text,
			MatchedSelector: parsed.Selector,
			InputVisible:    inputVis,
			ScrollAttempts:  attempt,
			ChatURL:         pageURLString(page),
		}
		if dryRun {
			if inputVis {
				return diag, convPaths, nil
			}
			return diag, convPaths, errors.New("dry_run：已匹配并点击群名，但未检测到消息输入框")
		}
		if !inputVis {
			return diag, convPaths, errors.New("已点击群聊但未发现消息输入框")
		}
		return diag, convPaths, nil
	}

	return lastDiag, sendGroupDebugPaths{}, errors.Wrapf(formatGroupChatNotFoundError(want, nil), "左侧栏滚动 %d 次后仍未匹配或未能点击群聊", maxLeftPanelScrollForGroup+1)
}

func scrollLeftPanelDown(page *rod.Page) {
	_, _ = page.Eval(`(step) => {
		const sels = ['aside', 'nav', '[class*="session"]', '[class*="conversation"]', '[class*="chat-list"]', '[class*="msg-list"]'];
		let best = null;
		let bestArea = 0;
		for (const sel of sels) {
			for (const el of document.querySelectorAll(sel)) {
				const r = el.getBoundingClientRect();
				if (r.left > window.innerWidth * 0.45) continue;
				const area = r.width * r.height;
				if (area > bestArea && r.height > 120) {
					bestArea = area;
					best = el;
				}
			}
		}
		const target = best || document.documentElement;
		target.scrollBy(0, step);
	}`, leftPanelScrollStepPx)
}

// messageComposerVisible 与 dry_run / 发送路径共用：可见 textarea 或 contenteditable。
func messageComposerVisible(page *rod.Page) (bool, error) {
	res, err := page.Eval(`() => {
		const vis = (el) => {
			if (!el) return false;
			const st = window.getComputedStyle(el);
			if (st.display === 'none' || st.visibility === 'hidden' || parseFloat(st.opacity || '1') < 0.05) return false;
			const r = el.getBoundingClientRect();
			return r.width > 10 && r.height > 8;
		};
		for (const ta of document.querySelectorAll('textarea')) {
			if (vis(ta)) return true;
		}
		for (const ed of document.querySelectorAll('[contenteditable="true"]')) {
			if (vis(ed)) return true;
		}
		return false;
	}`)
	if err != nil {
		return false, err
	}
	return res.Value.Bool(), nil
}

func messageInputVisible(page *rod.Page) bool {
	ok, err := messageComposerVisible(page)
	if err != nil {
		return false
	}
	return ok
}

// findChatComposerElement 与 dry_run 的可见性逻辑一致：优先可见 textarea，其次 contenteditable；返回真实 DOM 节点供 Focus/InsertText。
func findChatComposerElement(page *rod.Page) (*rod.Element, error) {
	res, err := page.Evaluate(rod.Eval(`() => {
		const vis = (el) => {
			if (!el) return false;
			const st = window.getComputedStyle(el);
			if (st.display === 'none' || st.visibility === 'hidden' || parseFloat(st.opacity || '1') < 0.05) return false;
			const r = el.getBoundingClientRect();
			return r.width > 10 && r.height > 8;
		};
		for (const ta of document.querySelectorAll('textarea')) {
			if (vis(ta)) return ta;
		}
		for (const ed of document.querySelectorAll('[contenteditable="true"]')) {
			if (vis(ed)) return ed;
		}
		return null;
	}`).ByObject())
	if err != nil {
		return nil, err
	}
	if res == nil || res.Subtype != proto.RuntimeRemoteObjectSubtypeNode {
		return nil, errors.New("未找到可见文本输入框")
	}
	return page.ElementFromObject(res)
}

func findSendButton(page *rod.Page) (*rod.Element, error) {
	candidates := []string{
		`//button[contains(normalize-space(.),"发送")]`,
		`//div[@role="button"][contains(normalize-space(.),"发送")]`,
		`//*[self::button or self::div][contains(@class,"send")]`,
	}
	for _, xp := range candidates {
		if el, err := page.Timeout(8 * time.Second).Element(`xpath=` + xp); err == nil && el != nil {
			if vis, _ := el.Visible(); vis {
				return el, nil
			}
		}
	}
	return nil, errors.New("未找到发送按钮")
}

func groupImageMimeFromPath(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".png":
		return "image/png"
	default:
		return "application/octet-stream"
	}
}

func readImageFileBase64(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(b), nil
}

// messagePaneImageStats 右侧会话区图片数量：优先统计疑似己方（右侧对齐）气泡内的 img，同时保留整区右侧 img 数作回退。
func messagePaneImageStats(page *rod.Page) (userLike int, rightTotal int, err error) {
	res, err := page.Eval(`() => {
		const split = window.innerWidth * 0.32;
		const roots = [];
		const pv = document.querySelector('.xhs-im-view');
		if (pv) roots.push(pv);
		roots.push(document.body);
		let userLike = 0;
		let rightTotal = 0;
		const seen = new Set();
		for (const root of roots) {
			for (const im of root.querySelectorAll('img')) {
				if (seen.has(im)) continue;
				const r = im.getBoundingClientRect();
				if (r.left < split || r.width < 8 || r.height < 8) continue;
				if (r.bottom < 0 || r.top > window.innerHeight) continue;
				seen.add(im);
				rightTotal++;
				let p = im;
				let selfSide = false;
				for (let d = 0; d < 16 && p; d++) {
					const cls = (p.className && String(p.className)) || '';
					const st = window.getComputedStyle(p);
					if (cls.includes('self') || cls.includes('mine') || cls.includes('sender-self') || cls.includes('msg-self') ||
						st.alignSelf === 'flex-end' || (st.marginLeft === 'auto' && st.marginRight !== 'auto')) {
						selfSide = true;
						break;
					}
					p = p.parentElement;
				}
				if (selfSide) userLike++;
			}
		}
		return JSON.stringify({ user_like: userLike, right_total: rightTotal });
	}`)
	if err != nil {
		return 0, 0, errors.Wrap(err, "统计会话区图片数量失败")
	}
	var parsed struct {
		UserLike   int `json:"user_like"`
		RightTotal int `json:"right_total"`
	}
	if jerr := json.Unmarshal([]byte(res.Value.String()), &parsed); jerr != nil {
		return 0, 0, jerr
	}
	return parsed.UserLike, parsed.RightTotal, nil
}

// injectImageDataTransferEval 将本地图片以 File 注入 DataTransfer，按 method 触发 paste 或 drop。
// method: "paste_editor" | "drop_editor" | "drop_view"
func injectImageDataTransferEval(page *rod.Page, b64, mime, fileName, method string) error {
	res, err := page.Eval(`(b64, mime, name, method) => {
		const bin = atob(b64);
		const bytes = new Uint8Array(bin.length);
		for (let i = 0; i < bin.length; i++) bytes[i] = bin.charCodeAt(i);
		const blob = new Blob([bytes], { type: mime || 'image/jpeg' });
		const file = new File([blob], name || 'image.jpg', { type: mime || 'image/jpeg' });
		const dt = new DataTransfer();
		dt.items.add(file);

		const editor = document.querySelector('.xhs-im-input-bar-editor[contenteditable="true"]') ||
			document.querySelector('[contenteditable="true"]');
		const view = document.querySelector('.xhs-im-view') ||
			document.querySelector('[class*="im-view"]') ||
			document.querySelector('main');

		const dispatchPaste = (target) => {
			if (!target) return { ok: false, err: 'no paste target' };
			target.focus();
			const ev = new ClipboardEvent('paste', { bubbles: true, cancelable: true, clipboardData: dt });
			const dispatched = target.dispatchEvent(ev);
			return { ok: true, dispatched };
		};

		const dispatchDrop = (target) => {
			if (!target) return { ok: false, err: 'no drop target' };
			const r = target.getBoundingClientRect();
			const clientX = r.left + Math.min(r.width * 0.5, 120);
			const clientY = r.top + Math.min(r.height * 0.5, 120);
			target.focus && target.focus();
			const mk = (type) => new DragEvent(type, {
				bubbles: true,
				cancelable: true,
				clientX,
				clientY,
				dataTransfer: dt
			});
			target.dispatchEvent(mk('dragenter'));
			target.dispatchEvent(mk('dragover'));
			const dropOk = target.dispatchEvent(mk('drop'));
			return { ok: true, dispatched: dropOk };
		};

		let out;
		if (method === 'paste_editor') {
			out = dispatchPaste(editor);
		} else if (method === 'drop_editor') {
			out = dispatchDrop(editor);
		} else if (method === 'drop_view') {
			out = dispatchDrop(view || document.body);
		} else {
			return JSON.stringify({ ok: false, err: 'bad method' });
		}
		return JSON.stringify(out);
	}`, b64, mime, fileName, method)
	if err != nil {
		return errors.Wrap(err, "注入图片脚本执行失败")
	}
	var parsed struct {
		OK         bool   `json:"ok"`
		Err        string `json:"err"`
		Dispatched bool   `json:"dispatched"`
	}
	if uerr := json.Unmarshal([]byte(res.Value.String()), &parsed); uerr != nil {
		return uerr
	}
	if !parsed.OK {
		if strings.TrimSpace(parsed.Err) == "" {
			return errors.New("注入图片失败")
		}
		return errors.New(parsed.Err)
	}
	return nil
}

// clickImagePreviewOrConfirmButtons 若有预览弹窗，点击 发送/确定/确认。
func clickImagePreviewOrConfirmButtons(page *rod.Page) error {
	opCtx, opCancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer opCancel()
	op := page.Context(opCtx).Timeout(8 * time.Second)
	for attempt := 0; attempt < 4; attempt++ {
		res, err := op.Eval(`() => {
			const want = ['发送', '确定', '确认'];
			const nodes = Array.from(document.querySelectorAll('button, [role="button"], div, span'));
			for (const el of nodes) {
				const t = (el.innerText || el.textContent || '').trim();
				if (!t) continue;
				if (!want.some(w => t === w || t.includes(w))) continue;
				const r = el.getBoundingClientRect();
				if (r.width < 2 || r.height < 2) continue;
				el.click();
				return true;
			}
			return false;
		}`)
		if err == nil && res.Value.Bool() {
			humanize.Delay(opCtx, humanize.AfterClick)
			_ = softWaitStable(page, waitStableShort)
			return nil
		}
		time.Sleep(350 * time.Millisecond)
	}
	return nil
}

func confirmImageAfterInject(page *rod.Page) error {
	_ = clickImagePreviewOrConfirmButtons(page)
	opCtx, opCancel := context.WithTimeout(context.Background(), stepOperationTimeout)
	defer opCancel()
	op := page.Context(opCtx).Timeout(stepOperationTimeout)
	btn, btnErr := findSendButton(op)
	if btnErr == nil && btn != nil {
		humanize.Delay(opCtx, humanize.BeforeClick)
		if err := humanize.Click(btn); err != nil {
			return errors.Wrap(err, "点击发送图片失败")
		}
		humanize.Delay(opCtx, humanize.AfterClick)
		_ = softWaitStable(page, waitStableShort)
		return nil
	}
	kbPage := page.Context(opCtx)
	if err := kbPage.Keyboard.Type(input.Enter); err != nil {
		return errors.Wrap(err, "发送图片失败（无发送按钮且回车无效）")
	}
	humanize.Delay(opCtx, humanize.AfterInteract)
	_ = softWaitStable(page, waitStableShort)
	return nil
}

func waitImageMessageRenderedAfter(page *rod.Page, beforeUser, beforeRight int) error {
	deadline := time.Now().Add(sendVerifyTimeout)
	for time.Now().Before(deadline) {
		u, rtot, err := messagePaneImageStats(page)
		if err != nil {
			time.Sleep(400 * time.Millisecond)
			continue
		}
		if u > beforeUser {
			return nil
		}
		if rtot > beforeRight {
			return nil
		}
		time.Sleep(400 * time.Millisecond)
	}
	return errors.New("超时：会话区右侧未检测到新增图片消息")
}

func sendOneImageMessage(page *rod.Page, path string, debugDir string) error {
	b64, err := readImageFileBase64(path)
	if err != nil {
		return errors.Wrap(err, "读取本地图片失败")
	}
	mime := groupImageMimeFromPath(path)
	name := filepath.Base(path)

	beforeU, beforeR, err := messagePaneImageStats(page)
	if err != nil {
		return err
	}

	methods := []struct {
		tag string
		key string
	}{
		{"paste", "paste_editor"},
		{"drop_ed", "drop_editor"},
		{"drop_view", "drop_view"},
	}
	var attemptLines []string
	for _, m := range methods {
		stepCtx, stepCancel := context.WithTimeout(context.Background(), stepOperationTimeout)
		op := page.Context(stepCtx).Timeout(stepOperationTimeout)
		injErr := injectImageDataTransferEval(op, b64, mime, name, m.key)
		stepCancel()
		if injErr != nil {
			attemptLines = append(attemptLines, fmt.Sprintf("%s: 注入失败 — %v", m.tag, injErr))
			if strings.TrimSpace(debugDir) != "" {
				if _, se := saveSendGroupConvSnapshotAttempt(page, debugDir, "img_"+m.tag+"_fail"); se != nil {
					logrus.Warnf("图片发送失败快照: %v", se)
				}
			}
			continue
		}

		time.Sleep(400 * time.Millisecond)
		_ = confirmImageAfterInject(page)

		verifyCtx, verifyCancel := context.WithTimeout(context.Background(), sendVerifyTimeout)
		vp := page.Context(verifyCtx).Timeout(sendVerifyTimeout)
		waitErr := waitImageMessageRenderedAfter(vp, beforeU, beforeR)
		verifyCancel()

		if waitErr == nil {
			return nil
		}

		attemptLines = append(attemptLines, fmt.Sprintf("%s: 已注入但未在消息区确认 — %v", m.tag, waitErr))

		if strings.TrimSpace(debugDir) != "" {
			if _, se := saveSendGroupConvSnapshotAttempt(page, debugDir, "img_"+m.tag+"_noverify"); se != nil {
				logrus.Warnf("图片校验失败快照: %v", se)
			}
		}
		// 为下一次尝试刷新基线（页面可能已插入预览图）
		if u2, r2, e2 := messagePaneImageStats(page); e2 == nil {
			beforeU, beforeR = u2, r2
		}
	}

	return errors.Errorf("图片发送失败（顺序尝试 paste → drop_editor → drop_view）。%s", strings.Join(attemptLines, " | "))
}

func sendTextMessage(page *rod.Page, text string) error {
	stepCtx, cancel := context.WithTimeout(context.Background(), stepOperationTimeout)
	defer cancel()
	op := page.Context(stepCtx).Timeout(stepOperationTimeout)

	ti, err := findChatComposerElement(op)
	if err != nil {
		return errors.Wrap(err, "未找到文本输入框")
	}
	typeCtx, typeCancel := context.WithTimeout(context.Background(), stepOperationTimeout)
	defer typeCancel()
	if err := humanize.Type(typeCtx, ti, text); err != nil {
		return errors.Wrap(err, "输入文字失败")
	}
	humanize.Delay(typeCtx, humanize.AfterType)

	btn, btnErr := findSendButton(op)
	if btnErr == nil && btn != nil {
		humanize.Delay(typeCtx, humanize.BeforeClick)
		if err := humanize.Click(btn); err != nil {
			return errors.Wrap(err, "点击发送文字失败")
		}
		humanize.Delay(typeCtx, humanize.AfterClick)
	} else {
		kbPage := page.Context(typeCtx)
		if err := kbPage.Keyboard.Type(input.Enter); err != nil {
			return errors.Wrap(err, "发送文字失败（无发送按钮且回车无效）")
		}
		humanize.Delay(typeCtx, humanize.AfterInteract)
	}
	_ = softWaitStable(page, waitStableShort)

	verifyCtx, verifyCancel := context.WithTimeout(context.Background(), sendVerifyTimeout)
	defer verifyCancel()
	verifyPage := page.Context(verifyCtx).Timeout(sendVerifyTimeout)
	if err := waitMessageAppearsInMessagePane(verifyPage, text); err != nil {
		return errors.Wrap(err, "发送文字后未在会话中确认到消息")
	}
	return nil
}

// waitMessageAppearsInMessagePane 在右侧消息区域（非左侧列表）轮询 innerText 是否含发送内容。
func waitMessageAppearsInMessagePane(page *rod.Page, marker string) error {
	marker = strings.TrimSpace(marker)
	if marker == "" {
		return errors.New("消息内容为空")
	}
	deadline := time.Now().Add(sendVerifyTimeout)
	for time.Now().Before(deadline) {
		res, err := page.Eval(`(needle) => {
			const w = window.innerWidth * 0.32;
			let pane = null;
			for (const el of document.querySelectorAll('[class*="message"], [class*="msg"], main, section, article')) {
				const r = el.getBoundingClientRect();
				if (r.left >= w && r.width > 80 && r.height > 80) { pane = el; break; }
			}
			if (!pane) {
				for (const el of document.querySelectorAll('div')) {
					const r = el.getBoundingClientRect();
					if (r.left < w) continue;
					if (el.querySelectorAll('img, video, textarea, [contenteditable="true"]').length >= 1 && r.width > 120) {
						pane = el;
						break;
					}
				}
			}
			const t = (pane && (pane.innerText || pane.textContent)) || (document.body.innerText || '');
			return JSON.stringify({ hit: t.includes(needle), sampleLen: t.length });
		}`, marker)
		if err == nil {
			var parsed struct {
				Hit       bool `json:"hit"`
				SampleLen int  `json:"sampleLen"`
			}
			if json.Unmarshal([]byte(res.Value.String()), &parsed) == nil && parsed.Hit {
				return nil
			}
		}
		time.Sleep(400 * time.Millisecond)
	}
	return errors.Errorf("超时：右侧消息区未出现文字 %q", marker)
}
