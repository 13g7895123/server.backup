package backup

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// SystemConfig 對應 backup_targets.config (type=system)
type SystemConfig struct {
	Include        []string `json:"include"`
	Exclude        []string `json:"exclude"`
	BackupPackages bool     `json:"backup_packages"`
	BackupServices bool     `json:"backup_services"`
}

func ParseSystemConfig(raw json.RawMessage) (*SystemConfig, error) {
	var cfg SystemConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, err
	}
	if len(cfg.Include) == 0 {
		cfg.Include = []string{"/etc", "/home", "/var/www"}
	}
	if len(cfg.Exclude) == 0 {
		cfg.Exclude = []string{"/proc", "/sys", "/dev", "/tmp", "/run", "/mnt"}
	}
	return &cfg, nil
}

// BackupSystem 執行 Debian 系統備份
// 回傳 tar.gz 的 checksum、大小、錯誤
func BackupSystem(cfg *SystemConfig, destDir, timestamp string) (checksum string, size int64, err error) {
	if err := os.MkdirAll(destDir, 0755); err != nil {
		return "", 0, err
	}

	hostPrefix := os.Getenv("HOST_PREFIX") // e.g. "/host"

	// 1. 備份套件清單
	if cfg.BackupPackages {
		pkgFile := filepath.Join(destDir, fmt.Sprintf("packages_%s.txt", timestamp))
		if err := backupPackages(hostPrefix, pkgFile); err != nil {
			fmt.Printf("[system backup] 套件清單警告: %v\n", err)
		}
	}

	// 2. 備份服務清單
	if cfg.BackupServices {
		svcFile := filepath.Join(destDir, fmt.Sprintf("services_%s.txt", timestamp))
		backupServices(svcFile) //nolint
	}

	// 3. 打包目錄
	tarPath := filepath.Join(destDir, fmt.Sprintf("system_%s.tar.gz", timestamp))
	checksum, size, err = tarDirs(cfg, hostPrefix, tarPath)
	return
}

// backupPackages 解析 /var/lib/dpkg/status，不依賴 dpkg 指令
func backupPackages(hostPrefix, destFile string) error {
	statusPath := hostPrefix + "/var/lib/dpkg/status"
	f, err := os.Open(statusPath)
	if err != nil {
		return fmt.Errorf("無法開啟 dpkg status: %w", err)
	}
	defer f.Close()

	out, err := os.Create(destFile)
	if err != nil {
		return err
	}
	defer out.Close()

	scanner := bufio.NewScanner(f)
	var pkg, status string
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "Package: ") {
			pkg = strings.TrimPrefix(line, "Package: ")
		}
		if strings.HasPrefix(line, "Status: ") {
			status = line
		}
		if line == "" && pkg != "" {
			if strings.Contains(status, "install ok installed") {
				fmt.Fprintf(out, "%s\tinstall\n", pkg)
			}
			pkg, status = "", ""
		}
	}
	return scanner.Err()
}

// backupServices 呼叫 systemctl 或直接讀 /etc/systemd
func backupServices(destFile string) error {
	out, err := os.Create(destFile)
	if err != nil {
		return err
	}
	defer out.Close()

	cmd := exec.Command("systemctl", "list-units", "--all", "--no-pager", "--plain")
	cmd.Stdout = out
	if err := cmd.Run(); err != nil {
		// fallback: 列出 / etc/systemd/system 檔案名稱
		hostPrefix := os.Getenv("HOST_PREFIX")
		svcDir := hostPrefix + "/etc/systemd/system"
		entries, readErr := os.ReadDir(svcDir)
		if readErr != nil {
			return fmt.Errorf("systemctl 及 fallback 均失敗: %w", err)
		}
		for _, e := range entries {
			fmt.Fprintln(out, e.Name())
		}
	}
	return nil
}

// tarDirs 打包多個目錄成單一 tar.gz
func tarDirs(cfg *SystemConfig, hostPrefix, destPath string) (string, int64, error) {
	outFile, err := os.Create(destPath)
	if err != nil {
		return "", 0, err
	}
	defer outFile.Close()

	hash := sha256.New()
	mw := io.MultiWriter(outFile, hash)
	gw, _ := gzip.NewWriterLevel(mw, gzip.BestSpeed)
	tw := tar.NewWriter(gw)

	excludeSet := make(map[string]struct{})
	for _, ex := range cfg.Exclude {
		excludeSet[hostPrefix+ex] = struct{}{}
	}

	for _, inc := range cfg.Include {
		src := hostPrefix + inc
		if _, err := os.Stat(src); os.IsNotExist(err) {
			continue
		}

		if err := filepath.WalkDir(src, func(path string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return nil // 跳過無法存取的路徑
			}
			if _, skip := excludeSet[path]; skip {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}

			// 去掉 hostPrefix，保持原始路徑結構
			archiveName := strings.TrimPrefix(path, hostPrefix)
			if archiveName == "" {
				archiveName = "/"
			}

			info, err := d.Info()
			if err != nil {
				return nil
			}

			link := ""
			if info.Mode()&os.ModeSymlink != 0 {
				link, _ = os.Readlink(path)
			}

			hdr, err := tar.FileInfoHeader(info, link)
			if err != nil {
				return nil
			}
			hdr.Name = archiveName

			if err := tw.WriteHeader(hdr); err != nil {
				return err
			}

			if !d.IsDir() && info.Mode().IsRegular() {
				f, err := os.Open(path)
				if err != nil {
					return nil // 權限不足就跳過
				}
				defer f.Close()
				io.Copy(tw, f) //nolint
			}
			return nil
		}); err != nil {
			fmt.Printf("[system backup] WalkDir 警告 %s: %v\n", src, err)
		}
	}

	tw.Close()
	gw.Close()

	stat, _ := outFile.Stat()
	return hex.EncodeToString(hash.Sum(nil)), stat.Size(), nil
}
