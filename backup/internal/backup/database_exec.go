package backup

import (
	"fmt"
	"io"
	"os/exec"
	"strings"
)

// dumpViaDockerExec 透過 docker exec 在目標 container 內執行 dump，
// 不需在本機安裝 pg_dump / mysqldump，適合 host agent 直接備份其他 container。
func dumpViaDockerExec(containerName, dbType string, cfg *DatabaseConfig, password string, w io.Writer) error {
	var args []string
	switch dbType {
	case "postgres":
		args = []string{"exec"}
		if password != "" {
			args = append(args, "-e", "PGPASSWORD="+password)
		}
		args = append(args, containerName,
			"pg_dump", "-U", cfg.User, "-d", cfg.Name, "--no-password", "-Fp")
	case "mysql":
		args = []string{"exec", containerName, "mysqldump",
			"--user=" + cfg.User,
			"--single-transaction", "--routines", "--triggers", "--no-tablespaces"}
		if password != "" {
			args = append(args, "--password="+password)
		}
		args = append(args, cfg.Name)
	default:
		return fmt.Errorf("docker exec dump 不支援的資料庫類型: %s", dbType)
	}

	cmd := exec.Command("docker", args...)
	cmd.Stdout = w

	stderrBuf := &strings.Builder{}
	cmd.Stderr = stderrBuf

	// 展示完整指令（陰藏密碼）
	safeArgs := make([]string, 0, len(args))
	for i, a := range args {
		if i > 0 && (args[i-1] == "-e") && strings.HasPrefix(a, "PGPASSWORD=") {
			safeArgs = append(safeArgs, "-e", "PGPASSWORD=***")
			continue
		}
		if strings.HasPrefix(a, "--password=") {
			safeArgs = append(safeArgs, "--password=***")
			continue
		}
		safeArgs = append(safeArgs, a)
	}
	fullCmd := "docker " + strings.Join(safeArgs, " ")

	if err := cmd.Run(); err != nil {
		stderr := strings.TrimSpace(stderrBuf.String())
		if stderr != "" {
			return fmt.Errorf("[cmd] %s\n[error] %w\n[stderr] %s", fullCmd, err, stderr)
		}
		return fmt.Errorf("[cmd] %s\n[error] %w", fullCmd, err)
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

	var pgStderrBuf strings.Builder
	cmd.Stderr = &pgStderrBuf

	fullCmd := "pg_dump " + strings.Join(args, " ")

	if err := cmd.Run(); err != nil {
		stderr := strings.TrimSpace(pgStderrBuf.String())
		if stderr != "" {
			return fmt.Errorf("[cmd] %s\n[error] %w\n[stderr] %s", fullCmd, err, stderr)
		}
		return fmt.Errorf("[cmd] %s\n[error] %w", fullCmd, err)
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

	var myStderrBuf strings.Builder
	cmd.Stderr = &myStderrBuf

	safeMysqlArgs := make([]string, 0, len(args))
	for _, a := range args {
		if strings.HasPrefix(a, "--password=") {
			safeMysqlArgs = append(safeMysqlArgs, "--password=***")
		} else {
			safeMysqlArgs = append(safeMysqlArgs, a)
		}
	}
	fullMysqlCmd := "mysqldump " + strings.Join(safeMysqlArgs, " ")

	if err := cmd.Run(); err != nil {
		stderr := strings.TrimSpace(myStderrBuf.String())
		if stderr != "" {
			return fmt.Errorf("[cmd] %s\n[error] %w\n[stderr] %s", fullMysqlCmd, err, stderr)
		}
		return fmt.Errorf("[cmd] %s\n[error] %w", fullMysqlCmd, err)
	}
	return nil
}
