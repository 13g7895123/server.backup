package backup

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// FilesConfig 對應 backup_targets.config (type=files)
type FilesConfig struct {
	Source   string   `json:"source"`
	Compress string   `json:"compress"` // "gzip"
	Exclude  []string `json:"exclude"`
}

// BackupFiles 使用純 Go 打包來源目錄，寫入 destPath(.tar.gz)
// 回傳 sha256、檔案大小、錯誤
func BackupFiles(cfg FilesConfig, destPath string) (checksum string, size int64, err error) {
	// 容器內的 Host mount prefix（預設 /host）
	hostPrefix := os.Getenv("HOST_PREFIX")
	src := hostPrefix + cfg.Source

	if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
		return "", 0, fmt.Errorf("建立目標目錄失敗: %w", err)
	}

	outFile, err := os.Create(destPath)
	if err != nil {
		return "", 0, fmt.Errorf("建立備份檔案失敗: %w", err)
	}
	defer outFile.Close()

	// sha256 同步計算
	hash := sha256.New()
	mw := io.MultiWriter(outFile, hash)

	gw, _ := gzip.NewWriterLevel(mw, gzip.BestSpeed)
	tw := tar.NewWriter(gw)

	excludeSet := make(map[string]struct{}, len(cfg.Exclude))
	for _, ex := range cfg.Exclude {
		excludeSet[ex] = struct{}{}
	}

	walkErr := filepath.WalkDir(src, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}

		relPath, _ := filepath.Rel(filepath.Dir(src), path)

		// 排除規則
		base := filepath.Base(path)
		if _, skip := excludeSet[base]; skip {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		info, err := d.Info()
		if err != nil {
			return err
		}

		// 處理 symlink
		link := ""
		if info.Mode()&os.ModeSymlink != 0 {
			link, _ = os.Readlink(path)
		}

		hdr, err := tar.FileInfoHeader(info, link)
		if err != nil {
			return err
		}
		hdr.Name = relPath

		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}

		if !d.IsDir() && info.Mode().IsRegular() {
			f, err := os.Open(path)
			if err != nil {
				return err
			}
			defer f.Close()
			if _, err := io.Copy(tw, f); err != nil {
				return err
			}
		}
		return nil
	})

	tw.Close()
	gw.Close()

	if walkErr != nil {
		os.Remove(destPath)
		return "", 0, walkErr
	}

	stat, _ := outFile.Stat()
	return hex.EncodeToString(hash.Sum(nil)), stat.Size(), nil
}
