//go:build integration

// 集成测试：需登录态 + 触网。只上传视频并设置封面，不填标题、不点「发布」，不产生笔记。
//
//	COOKIES_PATH=/path/cookies.json XHS_TEST_VIDEO=/path/v.mp4 XHS_TEST_COVER=/path/c.jpg \
//	  go test -tags integration ./xiaohongshu/ -run TestSetCustomVideoCover -v -timeout 15m
package xiaohongshu

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/xpzouying/xiaohongshu-mcp/browser"
)

func TestSetCustomVideoCover(t *testing.T) {
	video := os.Getenv("XHS_TEST_VIDEO")
	cover := os.Getenv("XHS_TEST_COVER")
	if video == "" || cover == "" {
		t.Skip("需要 XHS_TEST_VIDEO 和 XHS_TEST_COVER")
	}

	b := browser.NewBrowser(true)
	defer b.Close()

	page := b.NewPage().Timeout(8 * time.Minute)
	defer page.Close()

	action, err := NewPublishVideoAction(page)
	require.NoError(t, err)
	require.NoError(t, uploadVideo(action.page, video))

	ctx := context.Background()
	require.NoError(t, setCustomVideoCover(ctx, action.page, cover))

	// 弹窗已关闭，且封面缩略图的背景图换成了新上传的图
	has, _, err := action.page.Has(coverModalSelector)
	require.NoError(t, err)
	require.False(t, has, "封面弹窗应已关闭")

	if shot := os.Getenv("XHS_TEST_SHOT"); shot != "" {
		data, err := action.page.Screenshot(false, nil)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(shot, data, 0o644))
	}
}
