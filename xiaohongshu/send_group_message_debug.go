package xiaohongshu

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-rod/rod"
	"github.com/pkg/errors"
)

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
	paths, saveErr := saveSendGroupMessageDebug(page, debugDir)
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

// saveSendGroupExploreSnapshot 在尝试进入 IM 之前保存 explore 页快照（文件名带 _explore）。
func saveSendGroupExploreSnapshot(page *rod.Page, debugDir string) (sendGroupDebugPaths, error) {
	return saveSendGroupPageSnapshot(page, debugDir, "_explore")
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
