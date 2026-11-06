package xiaohongshu

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-rod/rod"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
	"github.com/xpzouying/xiaohongshu-mcp/humanize"
)

// ValidateVideoCoverPath 校验可选封面路径：空则跳过；非空则须存在且为 jpg/png。
func ValidateVideoCoverPath(path string) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil
	}
	fi, err := os.Stat(path)
	if err != nil {
		return errors.Errorf("封面文件不存在或不可访问: %v", err)
	}
	if fi.IsDir() {
		return errors.New("封面路径指向目录，需要 jpg/png 图片文件")
	}
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".jpg", ".jpeg", ".png":
		return nil
	default:
		return errors.Errorf("封面仅支持 jpg/png，当前扩展名: %q", ext)
	}
}

// setCustomVideoCover 在视频上传完成后打开封面编辑器，上传本地封面并确认。
func setCustomVideoCover(ctx context.Context, page *rod.Page, coverPath string) error {
	coverPath = strings.TrimSpace(coverPath)
	if coverPath == "" {
		return nil
	}
	if err := ValidateVideoCoverPath(coverPath); err != nil {
		return err
	}

	pp := page.Context(ctx).Timeout(120 * time.Second)

	if err := clickVideoCoverEntry(pp); err != nil {
		return errors.Wrap(err, "打开视频封面设置失败")
	}
	time.Sleep(500 * time.Millisecond)

	if err := uploadCoverInModal(pp, coverPath); err != nil {
		return errors.Wrap(err, "上传自定义封面失败")
	}

	if err := clickCoverConfirmButton(pp); err != nil {
		return errors.Wrap(err, "确认封面失败")
	}

	if err := waitCoverEditorSettled(pp, 90*time.Second); err != nil {
		return errors.Wrap(err, "等待封面预览更新超时")
	}

	logrus.Info("视频自定义封面已设置")
	return nil
}

// 入口文案：「设置封面」或「修改封面」（XPath 1：用 | 合并）
const coverEntryXPath = `//*[contains(normalize-space(string(.)), '设置封面')] | //*[contains(normalize-space(string(.)), '修改封面')]`

func clickVideoCoverEntry(page *rod.Page) error {
	el, err := page.Timeout(45 * time.Second).Element(`xpath=` + coverEntryXPath)
	if err != nil {
		return errors.New("未找到「设置封面」或「修改封面」入口")
	}
	if !isElementVisible(el) {
		return errors.New("封面入口存在但不可见")
	}
	if err := el.ScrollIntoView(); err != nil {
		logrus.Debugf("封面入口滚动失败: %v", err)
	}
	time.Sleep(150 * time.Millisecond)
	return humanize.Click(el)
}

func uploadCoverInModal(page *rod.Page, coverPath string) error {
	if _, err := page.Timeout(30 * time.Second).Element(`xpath=//*[contains(normalize-space(string(.)), '上传封面')]`); err != nil {
		return errors.Wrap(err, "未出现封面编辑区（上传封面）")
	}
	inputs, err := page.Elements("input[type='file']")
	if err != nil {
		return errors.Wrap(err, "查找封面上传控件失败")
	}
	for _, inp := range inputs {
		acceptPtr, _ := inp.Attribute("accept")
		accept := ""
		if acceptPtr != nil {
			accept = *acceptPtr
		}
		if !acceptsImage(accept) {
			continue
		}
		// 封面 file 多为隐藏控件，不以 Visible 过滤
		if err := inp.SetFiles([]string{coverPath}); err != nil {
			return errors.Wrap(err, "写入封面文件失败")
		}
		logrus.Info("已选择封面文件上传")
		time.Sleep(800 * time.Millisecond)
		return nil
	}
	return errors.New("未找到接受图片的 file 输入框（上传封面）")
}

func clickCoverConfirmButton(page *rod.Page) error {
	deadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) {
		candidates, err := page.Elements("button")
		if err != nil {
			time.Sleep(200 * time.Millisecond)
			continue
		}
		for _, b := range candidates {
			if !isElementVisible(b) {
				continue
			}
			text, err := b.Text()
			if err != nil {
				continue
			}
			if !strings.Contains(strings.TrimSpace(text), "确定") {
				continue
			}
			parentText := ""
			if p, err := b.Parent(); err == nil && p != nil {
				pt, err := p.Text()
				if err == nil {
					parentText = pt
				}
			}
			// 避免点到正文工具栏等其它「确定」，要求与封面弹层同语境
			if strings.Contains(parentText, "上传封面") || strings.Contains(parentText, "封面") {
				return humanize.Click(b)
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	return errors.New("未找到封面编辑弹窗的「确定」按钮")
}

func waitCoverEditorSettled(page *rod.Page, maxWait time.Duration) error {
	deadline := time.Now().Add(maxWait)
	for time.Now().Before(deadline) {
		// 上传弹层关闭后，页面上不应再出现「上传封面」文案
		hasLabel, _, err := page.Has(`xpath=//*[contains(normalize-space(string(.)), '上传封面')]`)
		if err == nil && !hasLabel {
			time.Sleep(500 * time.Millisecond)
			return nil
		}
		// 备选：封面预览区出现图片（blob 或预览容器）
		hasPreview, _, err2 := page.Has(`img[src*="blob:"]`)
		if err2 == nil && hasPreview {
			time.Sleep(500 * time.Millisecond)
			return nil
		}
		time.Sleep(400 * time.Millisecond)
	}
	return errors.New("封面编辑未完成或预览未刷新")
}
