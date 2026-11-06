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
	if err == nil {
		return nil
	}
	if debugDir == "" || page == nil {
		return err
	}
	paths, saveErr := saveSendGroupMessageDebug(page, debugDir)
	if saveErr != nil {
		return errors.Wrapf(err, "另有调试文件写入失败: %v", saveErr)
	}
	return errors.Wrapf(err, "调试: screenshot=%s html=%s url_file=%s", paths.png, paths.html, paths.urlFile)
}

type sendGroupDebugPaths struct {
	png, html, urlFile string
}

func saveSendGroupMessageDebug(page *rod.Page, debugDir string) (sendGroupDebugPaths, error) {
	var out sendGroupDebugPaths
	if err := os.MkdirAll(debugDir, 0755); err != nil {
		return out, err
	}
	ts := time.Now().UnixMilli()
	base := filepath.Join(debugDir, fmt.Sprintf("send_group_message_debug_%d", ts))
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
