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
)

const debugSnapshotFreshTimeout = 15 * time.Second

// wrapWithSendGroupDebug 失败时把整页截图、HTML、最终 URL 存到 cookies 文件同目录，并在错误文案里带上路径。
func wrapWithSendGroupDebug(page *rod.Page, debugDir string, err error) error {
	return wrapWithSendGroupDebugExtra(page, debugDir, err, "")
}

// wrapWithSendGroupDebugExtra 同上，可附加前缀（例如 explore 快照路径与尝试 URL 列表）。
func wrapWithSendGroupDebugExtra(page *rod.Page, debugDir string, err error, extraPrefix string) error {
	if err == nil {
		return nil
	}
	if debugDir == "" || page == nil {
		if extraPrefix != "" {
			return errors.Errorf("%s: %v", extraPrefix, err)
		}
		return err
	}
	paths, saveErr := saveSendGroupMessageDebugFresh(page, debugDir)
	msg := fmt.Sprintf("调试: screenshot=%s html=%s url_file=%s", paths.png, paths.html, paths.urlFile)
	if saveErr != nil {
		msg = fmt.Sprintf("%s (调试保存部分失败: %v)", msg, saveErr)
	}
	if extraPrefix != "" {
		msg = extraPrefix + "; " + msg
	}
	return errors.Wrap(err, msg)
}

type sendGroupDebugPaths struct {
	png, html, urlFile string
}

func saveSendGroupMessageDebug(page *rod.Page, debugDir string) (sendGroupDebugPaths, error) {
	return saveSendGroupPageSnapshot(page, debugDir, "")
}

// saveSendGroupMessageDebugFresh 用独立短超时保存，避免外层 ctx 已过期导致截图/HTML 失败。
func saveSendGroupMessageDebugFresh(page *rod.Page, debugDir string) (sendGroupDebugPaths, error) {
	if page == nil {
		return sendGroupDebugPaths{}, errors.New("nil page")
	}
	ctx, cancel := context.WithTimeout(context.Background(), debugSnapshotFreshTimeout)
	defer cancel()
	p := page.Context(ctx)
	return saveSendGroupPageSnapshot(p, debugDir, "")
}

// saveSendGroupExploreSnapshot 在尝试进入 IM 之前保存 explore 页快照（文件名带 _explore）。
func saveSendGroupExploreSnapshot(page *rod.Page, debugDir string) (sendGroupDebugPaths, error) {
	return saveSendGroupPageSnapshotFresh(page, debugDir, "_explore")
}

// saveSendGroupChatSnapshot 进入网页私信 /chat 后保存快照（文件名带 _chat），供调整选择器。
func saveSendGroupChatSnapshot(page *rod.Page, debugDir string) (sendGroupDebugPaths, error) {
	return saveSendGroupPageSnapshotFresh(page, debugDir, "_chat")
}

// saveSendGroupConvSnapshot 点击会话打开右侧面板后保存（*_conv），成功/失败均调用。
func saveSendGroupConvSnapshot(page *rod.Page, debugDir string) (sendGroupDebugPaths, error) {
	return saveSendGroupPageSnapshotFresh(page, debugDir, "_conv")
}

// saveSendGroupConvSnapshotAttempt 打开会话后的调试快照；attemptTag 非空时文件名为 *_conv_<tag>（每次尝试可区分）。
func saveSendGroupConvSnapshotAttempt(page *rod.Page, debugDir string, attemptTag string) (sendGroupDebugPaths, error) {
	suffix := "_conv"
	if strings.TrimSpace(attemptTag) != "" {
		suffix = "_conv_" + strings.TrimSpace(attemptTag)
	}
	return saveSendGroupPageSnapshotFresh(page, debugDir, suffix)
}

func saveSendGroupPageSnapshotFresh(page *rod.Page, debugDir string, nameSuffix string) (sendGroupDebugPaths, error) {
	if page == nil {
		return sendGroupDebugPaths{}, errors.New("nil page")
	}
	ctx, cancel := context.WithTimeout(context.Background(), debugSnapshotFreshTimeout)
	defer cancel()
	p := page.Context(ctx)
	return saveSendGroupPageSnapshot(p, debugDir, nameSuffix)
}

func saveSendGroupPageSnapshot(page *rod.Page, debugDir string, nameSuffix string) (sendGroupDebugPaths, error) {
	var out sendGroupDebugPaths
	if err := os.MkdirAll(debugDir, 0755); err != nil {
		return out, err
	}
	ts := time.Now().UnixMilli()
	base := filepath.Join(debugDir, fmt.Sprintf("send_group_message_debug_%d%s", ts, nameSuffix))
	out.png = base + ".png"
	out.html = base + ".html"
	out.urlFile = base + "_url.txt"

	pngData, errShot := page.Screenshot(true, nil)
	if errShot == nil && len(pngData) > 0 {
		_ = os.WriteFile(out.png, pngData, 0644)
	}

	html, errHTML := page.HTML()
	if errHTML == nil && html != "" {
		_ = os.WriteFile(out.html, []byte(html), 0644)
	}

	info, errInfo := page.Info()
	urlLine := "(无法读取 page.Info)"
	if errInfo == nil && info != nil {
		urlLine = strings.TrimSpace(info.URL)
	}
	_ = os.WriteFile(out.urlFile, []byte(urlLine+"\n"), 0644)

	if errShot != nil && errHTML != nil {
		return out, errors.Errorf("截图与 HTML 均失败: %v; %v", errShot, errHTML)
	}
	return out, nil
}
