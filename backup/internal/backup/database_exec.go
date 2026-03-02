package backup

import (
	"fmt"
	"io"
	"os/exec"
)

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
	cmd.Env = append(cmd.Environ(), "PGPASSWORD="+password)
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
		fmt.Sprintf("--password=%s", password),
		"--single-transaction",
		"--routines",
		"--triggers",
		"--no-tablespaces",
		cfg.Name,
	}
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
