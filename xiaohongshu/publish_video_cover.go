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

// 创作者中心「设置封面」当前 DOM（2026-11 实测）：
//   - 页面上的「设置封面」只是区块标题 span，不可点击。
//   - 封面缩略图 `.cover-plugin-preview .cover .default`，鼠标悬停后才出现
//     `.cover-edit-entry`（文案「编辑封面」，未悬停时 0×0）。
//   - 点击后弹出 `.cover-modal.main-cover-editor-modal`，里面没有「上传封面」文案：
//     上传入口是 `label.upload-btn`（文案「上传」）内的 `input[type=file]`
//     （accept="image/png, image/jpeg, image/*"），上传成功后出现 `button.uploaded-thumbnail`。
//   - 确认按钮是弹窗里的「完成」（不是「确定」）。
//   - 首次进入时可能有「视频可以PK封面啦」引导气泡（按钮「我知道了」）盖住封面区。
const (
	coverTileSelector   = `.cover-plugin-preview .cover .default`
	coverEntrySelector  = `.cover-edit-entry`
	coverModalSelector  = `.cover-modal`
	coverInputSelector  = `.cover-modal .upload-btn input[type='file']`
	coverUploadedThumb  = `.cover-modal .uploaded-thumbnail`
	coverGuideDismissJS = `() => {
		const b = [...document.querySelectorAll('button, span, div')].find(e =>
			e.children.length <= 1 && e.textContent.trim() === '我知道了');
		if (!b) return false;
		b.click();
		return true;
	}`
)

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

	dismissCoverGuide(pp)

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

// dismissCoverGuide 尽力关掉「PK封面」新手引导气泡，失败不影响主流程。
func dismissCoverGuide(page *rod.Page) {
	if _, err := page.Timeout(3 * time.Second).Eval(coverGuideDismissJS); err != nil {
		logrus.Debugf("关闭封面引导失败（忽略）: %v", err)
		return
	}
	time.Sleep(800 * time.Millisecond)
}

// clickVideoCoverEntry 悬停封面缩略图，等「编辑封面」浮出后点击。
func clickVideoCoverEntry(page *rod.Page) error {
	tile, err := page.Timeout(45 * time.Second).Element(coverTileSelector)
	if err != nil {
		return errors.Wrap(err, "未找到封面缩略图（.cover-plugin-preview .cover .default）")
	}
	if err := tile.ScrollIntoView(); err != nil {
		logrus.Debugf("封面缩略图滚动失败: %v", err)
	}
	time.Sleep(150 * time.Millisecond)

	// 入口只在悬停时出现；悬停 + 轮询，最多重试几次
	var lastErr error
	for i := 0; i < 5; i++ {
		if err := humanize.Hover(tile); err != nil {
			lastErr = err
			time.Sleep(300 * time.Millisecond)
			continue
		}
		time.Sleep(400 * time.Millisecond)

		entry, err := tile.Timeout(3 * time.Second).Element(coverEntrySelector)
		if err != nil {
			lastErr = errors.Wrap(err, "悬停后未出现「编辑封面」")
			continue
		}
		if err := humanize.Click(entry); err != nil {
			lastErr = err
			continue
		}
		return nil
	}
	return errors.Wrap(lastErr, "未找到「编辑封面」入口")
}

func uploadCoverInModal(page *rod.Page, coverPath string) error {
	if _, err := page.Timeout(30 * time.Second).Element(coverModalSelector); err != nil {
		return errors.Wrap(err, "未出现封面编辑弹窗（.cover-modal）")
	}
	inp, err := page.Timeout(15 * time.Second).Element(coverInputSelector)
	if err != nil {
		return errors.Wrap(err, "未找到封面弹窗内的图片 file 输入框")
	}
	// file 输入框是 label.upload-btn 里的隐藏控件，不以 Visible 过滤
	if err := inp.SetFiles([]string{coverPath}); err != nil {
		return errors.Wrap(err, "写入封面文件失败")
	}
	logrus.Info("已选择封面文件上传")

	// 上传成功后弹窗底部会出现 .uploaded-thumbnail，并自动切换为该图为当前封面
	if _, err := page.Timeout(30 * time.Second).Element(coverUploadedThumb); err != nil {
		return errors.Wrap(err, "封面上传后未出现缩略图")
	}
	time.Sleep(800 * time.Millisecond)
	return nil
}

// clickCoverConfirmButton 点击封面弹窗右下角「完成」。
func clickCoverConfirmButton(page *rod.Page) error {
	deadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) {
		btns, err := page.Elements(coverModalSelector + ` button`)
		if err != nil {
			time.Sleep(200 * time.Millisecond)
			continue
		}
		for _, b := range btns {
			if !isElementVisible(b) {
				continue
			}
			text, err := b.Text()
			if err != nil || strings.TrimSpace(text) != "完成" {
				continue
			}
			return humanize.Click(b)
		}
		time.Sleep(200 * time.Millisecond)
	}
	return errors.New("未找到封面编辑弹窗的「完成」按钮")
}

// waitCoverEditorSettled 等封面弹窗关闭。
func waitCoverEditorSettled(page *rod.Page, maxWait time.Duration) error {
	deadline := time.Now().Add(maxWait)
	for time.Now().Before(deadline) {
		has, modal, err := page.Has(coverModalSelector)
		if err == nil && !has {
			time.Sleep(500 * time.Millisecond)
			return nil
		}
		if err == nil && modal != nil {
			if vis, verr := modal.Visible(); verr == nil && !vis {
				time.Sleep(500 * time.Millisecond)
				return nil
			}
		}
		time.Sleep(400 * time.Millisecond)
	}
	return errors.New("封面编辑未完成：弹窗未关闭")
}
