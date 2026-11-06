package xiaohongshu

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidateVideoCoverPath(t *testing.T) {
	t.Run("empty_ok", func(t *testing.T) {
		require.NoError(t, ValidateVideoCoverPath(""))
		require.NoError(t, ValidateVideoCoverPath("   "))
	})

	t.Run("missing_file", func(t *testing.T) {
		err := ValidateVideoCoverPath("/nonexistent/path/to/cover.jpg")
		require.Error(t, err)
	})

	t.Run("directory_rejected", func(t *testing.T) {
		dir := t.TempDir()
		err := ValidateVideoCoverPath(dir)
		require.Error(t, err)
	})

	t.Run("wrong_extension", func(t *testing.T) {
		f := filepath.Join(t.TempDir(), "x.gif")
		require.NoError(t, os.WriteFile(f, []byte("x"), 0o644))
		err := ValidateVideoCoverPath(f)
		require.Error(t, err)
	})

	t.Run("jpg_png_ok", func(t *testing.T) {
		for _, ext := range []string{".jpg", ".jpeg", ".PNG"} {
			f := filepath.Join(t.TempDir(), "c"+ext)
			require.NoError(t, os.WriteFile(f, []byte("x"), 0o644))
			require.NoError(t, ValidateVideoCoverPath(f))
		}
	})
}
