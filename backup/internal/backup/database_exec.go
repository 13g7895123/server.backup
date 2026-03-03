package backup

import (
	"fmt"
	"io"
	"os/exec"
)

// dumpViaDockerExec 透過 docker exec 在目標 container 內執行 dump，
// 不需在本機安裝 pg_dump / mysqldump，適合 host agent 直接備份其他 container。
func dumpViaDockerExec(containerName, dbType string, cfg *DatabaseConfig, password string, w io.Writer) error {
	var cmd *exec.Cmd
	switch dbType {
	case "postgres":
		args := []string{"exec"}
		if password != "" {
			args = append(args, "-e", "PGPASSWORD="+password)
		}
		args = append(args, containerName,
			"pg_dump", "-U", cfg.User, "-d", cfg.Name, "--no-password", "-Fp")
		cmd = exec.Command("docker", args...)
	case "mysql":
		args := []string{"exec", containerName, "mysqldump",
			"--user=" + cfg.User,
			"--single-transaction", "--routines", "--triggers", "--no-tablespaces"}
		if password != "" {
			args = append(args, "--password="+password)
		}
		args = append(args, cfg.Name)
		cmd = exec.Command("docker", args...)
	default:
		return fmt.Errorf("docker exec dump 不支援的資料庫類型: %s", dbType)
	}

	cmd.Stdout = w
	if stderr, err := cmd.StderrPipe(); err == nil {
		go io.Copy(io.Discard, stderr)
	}
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("docker exec %s (%s) dump 失敗: %w", containerName, dbType, err)
	}
	return nil
}

// dumpPostgres 透過 pg_dump 匯出並寫入 writer
func dumpPostgres(cfg *DatabaseConfig, password string, w io.Writer) error {
	args := []string{
		"-h", cfg.Host,
		"-p", fmt.Sprintf("%d", cfg.Port),
		"-U", cfg.User,
		"-d", cfg.Name,
		"--no-password",
		"-Fp", // plain SQL format
	}
	cmd := exec.Command("pg_dump", args...)
	env := cmd.Environ()
	if password != "" {
		env = append(env, "PGPASSWORD="+password)
	}
	cmd.Env = env
	cmd.Stdout = w

	if out, err := cmd.StderrPipe(); err == nil {
		go io.Copy(io.Discard, out)
	}

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("pg_dump 失敗: %w", err)
	}
	return nil
}

// dumpMySQL 透過 mysqldump 匯出並寫入 writer
func dumpMySQL(cfg *DatabaseConfig, password string, w io.Writer) error {
	args := []string{
		fmt.Sprintf("--host=%s", cfg.Host),
		fmt.Sprintf("--port=%d", cfg.Port),
		fmt.Sprintf("--user=%s", cfg.User),
		"--single-transaction",
		"--routines",
		"--triggers",
		"--no-tablespaces",
	}
	if password != "" {
		args = append(args, fmt.Sprintf("--password=%s", password))
	}
	args = append(args, cfg.Name)
	cmd := exec.Command("mysqldump", args...)
	cmd.Stdout = w

	if out, err := cmd.StderrPipe(); err == nil {
		go io.Copy(io.Discard, out)
	}

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("mysqldump 失敗: %w", err)
	}
	return nil
}
