package xiaohongshu

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidateGroupMessageInput(t *testing.T) {
	dir := t.TempDir()
	okImg := filepath.Join(dir, "a.png")
	require.NoError(t, os.WriteFile(okImg, []byte("x"), 0o644))
	badExt := filepath.Join(dir, "b.gif")
	require.NoError(t, os.WriteFile(badExt, []byte("x"), 0o644))

	t.Run("empty_group", func(t *testing.T) {
		err := ValidateGroupMessageInput(GroupMessageInput{GroupChat: "", Text: "hi"})
		require.Error(t, err)
	})
	t.Run("needs_text_or_images", func(t *testing.T) {
		err := ValidateGroupMessageInput(GroupMessageInput{GroupChat: "G", DryRun: false})
		require.Error(t, err)
	})
	t.Run("dry_run_text_optional", func(t *testing.T) {
		err := ValidateGroupMessageInput(GroupMessageInput{GroupChat: "G", DryRun: true})
		require.NoError(t, err)
	})
	t.Run("missing_image", func(t *testing.T) {
		err := ValidateGroupMessageInput(GroupMessageInput{
			GroupChat:  "G",
			ImagePaths: []string{filepath.Join(dir, "nope.png")},
			DryRun:     false,
		})
		require.Error(t, err)
	})
	t.Run("bad_extension", func(t *testing.T) {
		err := ValidateGroupMessageInput(GroupMessageInput{
			GroupChat:  "G",
			ImagePaths: []string{badExt},
			DryRun:     false,
		})
		require.Error(t, err)
	})
	t.Run("ok_images_only", func(t *testing.T) {
		err := ValidateGroupMessageInput(GroupMessageInput{
			GroupChat:  "G",
			ImagePaths: []string{okImg},
			DryRun:     false,
		})
		require.NoError(t, err)
	})
	t.Run("ok_text_only", func(t *testing.T) {
		err := ValidateGroupMessageInput(GroupMessageInput{
			GroupChat: "G",
			Text:      "hello",
			DryRun:    false,
		})
		require.NoError(t, err)
	})
}
