package api

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"backup-manager/internal/store"
	"github.com/jackc/pgx/v5"
)

type releaseHandler struct {
	rootDir string
	store   *store.Store
}

type agentReleaseCapability struct {
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"`
	Workdir   string `json:"workdir"`
	Script    string `json:"script"`
	Builder   string `json:"builder,omitempty"`
	LogRef    string `json:"log_ref,omitempty"`
}

type agentReleaseFile struct {
	Name        string `json:"name"`
	OS          string `json:"os,omitempty"`
	Arch        string `json:"arch,omitempty"`
	SizeBytes   int64  `json:"size_bytes"`
	SHA256      string `json:"sha256"`
	DownloadURL string `json:"download_url,omitempty"`
}

type agentReleaseManifest struct {
	Version string             `json:"version"`
	BuiltAt time.Time          `json:"built_at"`
	Commit  string             `json:"commit"`
	LogRef  string             `json:"log_ref,omitempty"`
	Files   []agentReleaseFile `json:"files"`
}

type agentReleaseDetail struct {
	agentReleaseManifest
	InstallCommand  string              `json:"install_command"`
	UpgradeCommand  string              `json:"upgrade_command"`
	RegisteredAgent any                 `json:"registered_agent,omitempty"`
	AgentConfig     *agentConfigPayload `json:"agent_config,omitempty"`
}

var releaseVersionPattern = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

var agentReleaseBuildLimiter = make(chan struct{}, 1)

const agentReleaseImportMaxBytes = 2 << 30

type agentReleaseBuildRequest struct {
	Version       string              `json:"version"`
	RegisterAgent *agentConfigPayload `json:"register_agent,omitempty"`
}

func RegisterAgentReleaseRoutes(mux *http.ServeMux, s *store.Store) {
	h := &releaseHandler{rootDir: envOr("AGENT_RELEASES_DIR", "artifacts/backup-agent"), store: s}
	mux.HandleFunc("GET /api/admin/agent-releases/capability", h.capability)
	mux.HandleFunc("POST /api/admin/agent-releases/build", h.build)
	mux.HandleFunc("POST /api/admin/agent-releases/register-agent", h.registerAgent)
	mux.HandleFunc("POST /api/admin/agent-releases/import", h.importRelease)
	mux.HandleFunc("GET /api/admin/agent-releases", h.list)
	mux.HandleFunc("GET /api/admin/agent-releases/{version}", h.get)
	mux.HandleFunc("GET /api/admin/agent-releases/{version}/download/{file}", h.download)
}

func (h *releaseHandler) capability(w http.ResponseWriter, r *http.Request) {
	localCap := detectReleaseCapability()
	if localCap.Available {
		localCap.Builder = "dashboard-local"
		writeJSON(w, http.StatusOK, localCap)
		return
	}

	builder, err := h.resolveBuilderAgent(r.Context())
	if err != nil {
		localCap.Reason = strings.TrimSpace(localCap.Reason + "；fallback build agent 不可用: " + err.Error())
		writeJSON(w, http.StatusOK, localCap)
		return
	}
	cap, err := fetchAgentReleaseCapability(r.Context(), builder)
	if err != nil {
		writeJSON(w, http.StatusOK, agentReleaseCapability{
			Available: false,
			Reason:    fmt.Sprintf("無法連線 build agent %s: %v", builder.Code, err),
			Builder:   builder.Code,
		})
		return
	}
	cap.Builder = builder.Code
	writeJSON(w, http.StatusOK, cap)
}

func (h *releaseHandler) build(w http.ResponseWriter, r *http.Request) {
	var body agentReleaseBuildRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "無效的 JSON: "+err.Error())
		return
	}
	body.Version = strings.TrimSpace(body.Version)
	if body.Version == "" || !releaseVersionPattern.MatchString(body.Version) {
		writeError(w, http.StatusBadRequest, "version 格式不合法")
		return
	}

	logPath, logger, closeFn, err := newAgentReleaseLogger(body.Version)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "建立 agent release log 失敗: "+err.Error())
		return
	}
	defer closeFn()

	logger.Printf("release build request start version=%s remote=%s user_agent=%q", body.Version, r.RemoteAddr, r.UserAgent())

	releaseFn, err := acquireAgentReleaseBuildSlot(r.Context(), logger, body.Version)
	if err != nil {
		writeJSON(w, http.StatusRequestTimeout, map[string]any{
			"error":   err.Error(),
			"log_ref": logPath,
		})
		return
	}
	defer releaseFn()

	var (
		detail  *agentReleaseDetail
		builder *store.Agent
	)
	if capability := detectReleaseCapability(); capability.Available {
		logger.Printf("build release locally version=%s workdir=%s script=%s", body.Version, capability.Workdir, capability.Script)
		detail, err = buildReleaseLocally(r.Context(), h.rootDir, body.Version, r, logger, logPath)
	} else {
		builder, err = h.resolveBuilderAgent(r.Context())
		if err != nil {
			logger.Printf("resolve build agent failed version=%s error=%v", body.Version, err)
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"error":   capability.Reason + "；fallback build agent 不可用: " + err.Error(),
				"log_ref": logPath,
			})
			return
		}
		detail, err = h.buildReleaseViaAgent(r.Context(), body.Version, r, logger, logPath, builder)
	}
	if err != nil {
		logger.Printf("release build request failed version=%s error=%v", body.Version, err)
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"error":   err.Error(),
			"log_ref": logPath,
		})
		return
	}
	if body.RegisterAgent != nil {
		cfg, saved, err := h.registerReleaseAgent(r.Context(), body.RegisterAgent, r)
		if err != nil {
			logger.Printf("register release agent failed version=%s error=%v", body.Version, err)
			writeJSON(w, http.StatusInternalServerError, map[string]any{
				"error":   "release 已建立，但註冊 agent 失敗: " + err.Error(),
				"log_ref": logPath,
			})
			return
		}
		detail.AgentConfig = cfg
		detail.RegisteredAgent = saved
		logger.Printf("register release agent success version=%s code=%s", body.Version, cfg.Code)
	}
	logger.Printf("release build request success version=%s log_ref=%s", body.Version, logPath)
	writeJSON(w, http.StatusCreated, detail)
}

// registerAgent 單獨建立或更新 Agent 管理資料，不觸發 build。
func (h *releaseHandler) registerAgent(w http.ResponseWriter, r *http.Request) {
	var body agentConfigPayload
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "無效的 JSON: "+err.Error())
		return
	}
	cfg, saved, err := h.registerReleaseAgent(r.Context(), &body, r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"agent_config":     cfg,
		"registered_agent": saved,
	})
}

func (h *releaseHandler) list(w http.ResponseWriter, r *http.Request) {
	entries, err := os.ReadDir(h.rootDir)
	if os.IsNotExist(err) {
		writeJSON(w, http.StatusOK, []agentReleaseManifest{})
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	items := make([]agentReleaseManifest, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		manifest, err := h.readManifest(entry.Name())
		if err != nil {
			continue
		}
		items = append(items, *manifest)
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].BuiltAt.After(items[j].BuiltAt)
	})
	writeJSON(w, http.StatusOK, items)
}

func (h *releaseHandler) get(w http.ResponseWriter, r *http.Request) {
	version := r.PathValue("version")
	manifest, err := h.readManifest(version)
	if err != nil {
		writeError(w, http.StatusNotFound, "找不到 release")
		return
	}
	writeJSON(w, http.StatusOK, h.releaseDetailFromManifest(r, manifest))
}

func (h *releaseHandler) download(w http.ResponseWriter, r *http.Request) {
	version := r.PathValue("version")
	fileName := filepath.Base(r.PathValue("file"))
	fullPath := filepath.Join(h.rootDir, version, fileName)
	if _, err := os.Stat(fullPath); err != nil {
		writeError(w, http.StatusNotFound, "找不到檔案")
		return
	}
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", fileName))
	http.ServeFile(w, r, fullPath)
}

func (h *releaseHandler) importRelease(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, agentReleaseImportMaxBytes)
	if err := r.ParseMultipartForm(agentReleaseImportMaxBytes); err != nil {
		writeError(w, http.StatusBadRequest, "無效的 multipart form: "+err.Error())
		return
	}

	version := strings.TrimSpace(r.FormValue("version"))
	if version == "" || !releaseVersionPattern.MatchString(version) {
		writeError(w, http.StatusBadRequest, "version 格式不合法")
		return
	}

	file, _, err := r.FormFile("bundle")
	if err != nil {
		writeError(w, http.StatusBadRequest, "缺少 bundle 檔案")
		return
	}
	defer file.Close()

	logPath, logger, closeFn, err := newAgentReleaseLogger(version)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "建立 agent release log 失敗: "+err.Error())
		return
	}
	defer closeFn()

	logger.Printf("release import request start version=%s remote=%s user_agent=%q", version, r.RemoteAddr, r.UserAgent())

	releaseFn, err := acquireAgentReleaseBuildSlot(r.Context(), logger, version)
	if err != nil {
		writeJSON(w, http.StatusRequestTimeout, map[string]any{
			"error":   err.Error(),
			"log_ref": logPath,
		})
		return
	}
	defer releaseFn()

	detail, err := h.importReleaseBundle(r, version, file, logger, logPath)
	if err != nil {
		logger.Printf("release import request failed version=%s error=%v", version, err)
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error":   err.Error(),
			"log_ref": logPath,
		})
		return
	}

	logger.Printf("release import request success version=%s log_ref=%s", version, logPath)
	writeJSON(w, http.StatusCreated, detail)
}

func HandleAgentReleaseCapabilityDirect(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, detectReleaseCapability())
}

func HandleAgentReleaseBuildDirect(w http.ResponseWriter, r *http.Request) {
	var body agentReleaseBuildRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, `{"error":"invalid json"}`, http.StatusBadRequest)
		return
	}
	body.Version = strings.TrimSpace(body.Version)
	if body.Version == "" || !releaseVersionPattern.MatchString(body.Version) {
		http.Error(w, `{"error":"invalid version"}`, http.StatusBadRequest)
		return
	}

	logPath, logger, closeFn, err := newAgentReleaseLogger(body.Version)
	if err != nil {
		http.Error(w, `{"error":"failed to create build log"}`, http.StatusInternalServerError)
		return
	}
	defer closeFn()

	releaseFn, err := acquireAgentReleaseBuildSlot(r.Context(), logger, body.Version)
	if err != nil {
		writeJSON(w, http.StatusRequestTimeout, map[string]any{
			"error":   err.Error(),
			"log_ref": logPath,
		})
		return
	}
	defer releaseFn()

	detail, err := buildReleaseLocally(r.Context(), envOr("AGENT_RELEASES_DIR", "/var/lib/backup-agent/releases"), body.Version, r, logger, logPath)
	if err != nil {
		logger.Printf("agent direct build failed version=%s error=%v", body.Version, err)
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"error":   err.Error(),
			"log_ref": logPath,
		})
		return
	}
	writeJSON(w, http.StatusCreated, detail)
}

func HandleAgentReleaseDownloadDirect(w http.ResponseWriter, r *http.Request) {
	rootDir := envOr("AGENT_RELEASES_DIR", "/var/lib/backup-agent/releases")
	version := r.PathValue("version")
	fileName := filepath.Base(r.PathValue("file"))
	fullPath := filepath.Join(rootDir, version, fileName)
	if _, err := os.Stat(fullPath); err != nil {
		http.Error(w, `{"error":"file not found"}`, http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", fileName))
	http.ServeFile(w, r, fullPath)
}

func buildReleaseLocally(ctx context.Context, rootDir, version string, r *http.Request, logger *log.Logger, logPath string) (*agentReleaseDetail, error) {
	releaseDir := filepath.Join(rootDir, version)
	return buildReleaseFromCapability(ctx, releaseDir, version, r, logger, logPath)
}

func (h *releaseHandler) releaseDetailFromManifest(r *http.Request, manifest *agentReleaseManifest) agentReleaseDetail {
	detail := agentReleaseDetail{
		agentReleaseManifest: *manifest,
		InstallCommand:       h.installCommand(r, manifest.Version),
		UpgradeCommand:       h.installCommand(r, manifest.Version),
	}
	for i := range detail.Files {
		detail.Files[i].DownloadURL = fmt.Sprintf("%s/api/admin/agent-releases/%s/download/%s", baseURL(r), manifest.Version, detail.Files[i].Name)
	}
	return detail
}

func (h *releaseHandler) importReleaseBundle(r *http.Request, version string, src io.Reader, logger *log.Logger, logPath string) (*agentReleaseDetail, error) {
	releaseDir := filepath.Join(h.rootDir, version)
	if _, err := os.Stat(releaseDir); err == nil {
		return nil, fmt.Errorf("release %s 已存在", version)
	}

	if err := os.MkdirAll(h.rootDir, 0755); err != nil {
		return nil, fmt.Errorf("建立 release 根目錄失敗: %w", err)
	}

	tempDir, err := os.MkdirTemp(h.rootDir, "."+sanitizeReleaseToken(version)+"-import-")
	if err != nil {
		return nil, fmt.Errorf("建立暫存目錄失敗: %w", err)
	}
	success := false
	defer func() {
		if success {
			return
		}
		_ = os.RemoveAll(tempDir)
	}()

	if err := extractReleaseBundle(tempDir, version, src, logger); err != nil {
		return nil, err
	}

	manifest, err := validateImportedRelease(tempDir, version, logPath)
	if err != nil {
		return nil, err
	}

	if err := os.Rename(tempDir, releaseDir); err != nil {
		return nil, fmt.Errorf("移動匯入 release 失敗: %w", err)
	}
	success = true

	for i := range manifest.Files {
		manifest.Files[i].DownloadURL = fmt.Sprintf("%s/api/admin/agent-releases/%s/download/%s", baseURL(r), version, manifest.Files[i].Name)
	}
	detail := h.releaseDetailFromManifest(r, manifest)
	logger.Printf("release import complete version=%s files=%d", version, len(detail.Files))
	return &detail, nil
}

func (h *releaseHandler) registerReleaseAgent(ctx context.Context, body *agentConfigPayload, r *http.Request) (*agentConfigPayload, *store.Agent, error) {
	if body == nil {
		return nil, nil, nil
	}

	code := strings.TrimSpace(body.Code)
	name := strings.TrimSpace(body.Name)
	baseURL := strings.TrimSpace(body.BaseURL)
	token := strings.TrimSpace(body.Token)
	if code == "" || name == "" || baseURL == "" || token == "" {
		return nil, nil, fmt.Errorf("register_agent 的 name, code, base_url, token 不可為空")
	}

	template := &store.Agent{
		Name:      name,
		Code:      code,
		BaseURL:   baseURL,
		TokenHash: token,
		Enabled:   true,
		Status:    "offline",
	}

	cfg := buildAgentConfigPayload(template, body, r)
	current, err := h.store.GetAgentByCode(ctx, cfg.Code)
	if err == nil {
		current.Name = cfg.Name
		current.BaseURL = cfg.BaseURL
		current.TokenHash = cfg.Token
		current.Enabled = true
		if err := h.store.UpdateAgent(ctx, current); err != nil {
			return nil, nil, err
		}
		updated, err := h.store.GetAgent(ctx, current.ID)
		if err != nil {
			return nil, nil, err
		}
		return &cfg, updated, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, err
	}

	created, err := h.store.CreateAgent(ctx, &store.Agent{
		Name:      cfg.Name,
		Code:      cfg.Code,
		BaseURL:   cfg.BaseURL,
		TokenHash: cfg.Token,
		Enabled:   true,
		Status:    "offline",
	})
	if err != nil {
		return nil, nil, err
	}
	return &cfg, created, nil
}

func buildReleaseFromCapability(ctx context.Context, releaseDir, version string, r *http.Request, logger *log.Logger, logPath string) (*agentReleaseDetail, error) {
	capability := detectReleaseCapability()
	logger.Printf("capability available=%t workdir=%s script=%s reason=%q", capability.Available, capability.Workdir, capability.Script, capability.Reason)
	if !capability.Available {
		return nil, fmt.Errorf("agent release build 目前不可用: %s", capability.Reason)
	}

	logger.Printf("prepare release dir=%s", releaseDir)
	if _, err := os.Stat(releaseDir); err == nil {
		return nil, fmt.Errorf("release %s 已存在", version)
	}
	if err := os.MkdirAll(releaseDir, 0755); err != nil {
		return nil, fmt.Errorf("建立 release 目錄失敗: %w", err)
	}
	success := false
	defer func() {
		if success {
			return
		}
		if err := os.RemoveAll(releaseDir); err != nil {
			logger.Printf("cleanup failed release dir=%s error=%v", releaseDir, err)
			return
		}
		logger.Printf("cleanup incomplete release dir=%s", releaseDir)
	}()

	logger.Printf("run build script workdir=%s script=%s", capability.Workdir, capability.Script)
	if err := runBuildScript(ctx, capability.Workdir, capability.Script, version, logger); err != nil {
		return nil, err
	}

	binarySrc := filepath.Join(capability.Workdir, "backup-agent")
	binaryDst := filepath.Join(releaseDir, "backup-agent-linux-amd64")
	logger.Printf("copy binary src=%s dst=%s", binarySrc, binaryDst)
	if err := copyFile(binarySrc, binaryDst, 0755); err != nil {
		return nil, fmt.Errorf("複製 agent binary 失敗: %w", err)
	}

	extras := []string{"install-agent.sh", "diagnose-agent.sh", "backup-agent.service"}
	for _, name := range extras {
		src := filepath.Join(capability.Workdir, "scripts", name)
		dst := filepath.Join(releaseDir, name)
		mode := fs.FileMode(0644)
		if strings.HasSuffix(name, ".sh") {
			mode = 0755
		}
		logger.Printf("copy extra name=%s src=%s dst=%s", name, src, dst)
		if err := copyFile(src, dst, mode); err != nil {
			return nil, fmt.Errorf("複製 %s 失敗: %w", name, err)
		}
	}

	tarName := fmt.Sprintf("backup-agent_%s_linux_amd64.tar.gz", version)
	tarPath := filepath.Join(releaseDir, tarName)
	logger.Printf("write tar.gz path=%s", tarPath)
	if err := writeTarGz(tarPath, releaseDir, []string{
		"backup-agent-linux-amd64",
		"install-agent.sh",
		"diagnose-agent.sh",
		"backup-agent.service",
	}); err != nil {
		return nil, fmt.Errorf("建立 tar.gz 失敗: %w", err)
	}

	commit := gitCommit(ctx, capability.Workdir)
	files, err := buildReleaseFiles(releaseDir, []agentReleaseFile{
		{Name: "backup-agent-linux-amd64", OS: "linux", Arch: "amd64"},
		{Name: tarName, OS: "linux", Arch: "amd64"},
		{Name: "install-agent.sh"},
		{Name: "diagnose-agent.sh"},
		{Name: "backup-agent.service"},
	})
	if err != nil {
		return nil, err
	}

	manifest := agentReleaseManifest{
		Version: version,
		BuiltAt: time.Now().UTC(),
		Commit:  commit,
		LogRef:  logPath,
		Files:   files,
	}
	manifestPath := filepath.Join(releaseDir, "manifest.json")
	logger.Printf("write manifest path=%s", manifestPath)
	if err := writeJSONFile(manifestPath, manifest); err != nil {
		return nil, fmt.Errorf("寫入 manifest 失敗: %w", err)
	}

	checksumPath := filepath.Join(releaseDir, fmt.Sprintf("backup-agent_%s_checksums.txt", version))
	logger.Printf("write checksums path=%s", checksumPath)
	// Manifest 會記錄 checksum 檔本身的 hash，因此 checksum 不可再包含 manifest，
	// 否則兩個檔案會形成無法收斂的循環雜湊。
	checksumFiles := append([]agentReleaseFile{}, manifest.Files...)
	if err := writeChecksums(checksumPath, checksumFiles); err != nil {
		return nil, fmt.Errorf("寫入 checksums 失敗: %w", err)
	}
	checksumHash, checksumSize, err := sha256File(checksumPath)
	if err != nil {
		return nil, err
	}
	manifest.Files = append(manifest.Files, agentReleaseFile{
		Name:      filepath.Base(checksumPath),
		SizeBytes: checksumSize,
		SHA256:    checksumHash,
	})
	if err := writeJSONFile(manifestPath, manifest); err != nil {
		return nil, fmt.Errorf("最終寫入 manifest 失敗: %w", err)
	}

	detail := &agentReleaseDetail{
		agentReleaseManifest: manifest,
	}
	if r != nil {
		for i := range detail.Files {
			detail.Files[i].DownloadURL = fmt.Sprintf("%s/releases/%s/download/%s", baseURL(r), version, detail.Files[i].Name)
		}
	}
	success = true
	logger.Printf("release build complete version=%s commit=%s files=%d", version, commit, len(detail.Files))
	return detail, nil
}

func (h *releaseHandler) buildReleaseViaAgent(ctx context.Context, version string, r *http.Request, logger *log.Logger, logPath string, builder *store.Agent) (*agentReleaseDetail, error) {
	logger.Printf("selected build agent code=%s name=%s base_url=%s", builder.Code, builder.Name, builder.BaseURL)

	capability, err := fetchAgentReleaseCapability(ctx, builder)
	if err != nil {
		return nil, fmt.Errorf("查詢 build agent 能力失敗: %w", err)
	}
	logger.Printf("agent capability builder=%s available=%t workdir=%s script=%s reason=%q", builder.Code, capability.Available, capability.Workdir, capability.Script, capability.Reason)
	if !capability.Available {
		return nil, fmt.Errorf("agent release build 目前不可用: %s", capability.Reason)
	}

	detail, err := triggerAgentReleaseBuild(ctx, builder, version)
	if err != nil {
		return nil, err
	}

	releaseDir := filepath.Join(h.rootDir, version)
	logger.Printf("prepare local artifact dir=%s", releaseDir)
	if _, err := os.Stat(releaseDir); err == nil {
		return nil, fmt.Errorf("release %s 已存在", version)
	}
	if err := os.MkdirAll(releaseDir, 0755); err != nil {
		return nil, fmt.Errorf("建立 release 目錄失敗: %w", err)
	}
	success := false
	defer func() {
		if success {
			return
		}
		if err := os.RemoveAll(releaseDir); err != nil {
			logger.Printf("cleanup failed release dir=%s error=%v", releaseDir, err)
			return
		}
		logger.Printf("cleanup incomplete release dir=%s", releaseDir)
	}()

	for _, file := range detail.Files {
		dst := filepath.Join(releaseDir, file.Name)
		logger.Printf("download agent artifact name=%s dst=%s", file.Name, dst)
		if err := downloadAgentReleaseFile(ctx, builder, version, file.Name, dst); err != nil {
			return nil, fmt.Errorf("下載 agent artifact %s 失敗: %w", file.Name, err)
		}
	}

	manifest := detail.agentReleaseManifest
	manifest.LogRef = detail.LogRef
	if err := writeJSONFile(filepath.Join(releaseDir, "manifest.json"), manifest); err != nil {
		return nil, fmt.Errorf("寫入 manifest 失敗: %w", err)
	}

	result := &agentReleaseDetail{
		agentReleaseManifest: manifest,
		InstallCommand:       h.installCommand(r, version),
		UpgradeCommand:       h.installCommand(r, version),
	}
	for i := range result.Files {
		result.Files[i].DownloadURL = fmt.Sprintf("%s/api/admin/agent-releases/%s/download/%s", baseURL(r), version, result.Files[i].Name)
	}
	success = true
	logger.Printf("release mirror complete version=%s builder=%s files=%d", version, builder.Code, len(result.Files))
	return result, nil
}

func detectReleaseCapability() agentReleaseCapability {
	workdir := envOr("AGENT_BUILD_WORKDIR", ".")
	script := envOr("AGENT_BUILD_SCRIPT", filepath.Join("scripts", "build-agent.sh"))
	capability := agentReleaseCapability{
		Available: true,
		Workdir:   workdir,
		Script:    script,
	}

	info, err := os.Stat(workdir)
	if err != nil {
		capability.Available = false
		if os.IsNotExist(err) {
			capability.Reason = fmt.Sprintf("找不到 build workspace：%s。正式站若要建立版本，請把專案原始碼掛載到 dashboard，並設定 AGENT_BUILD_WORKDIR。", workdir)
			return capability
		}
		capability.Reason = fmt.Sprintf("無法讀取 build workspace %s：%v", workdir, err)
		return capability
	}
	if !info.IsDir() {
		capability.Available = false
		capability.Reason = fmt.Sprintf("AGENT_BUILD_WORKDIR 不是目錄：%s", workdir)
		return capability
	}

	scriptPath := script
	if !filepath.IsAbs(scriptPath) {
		scriptPath = filepath.Join(workdir, script)
	}
	scriptInfo, err := os.Stat(scriptPath)
	if err != nil {
		capability.Available = false
		if os.IsNotExist(err) {
			capability.Reason = fmt.Sprintf("build script 不存在：%s。請確認 dashboard 掛載的 workspace 內包含 scripts/build-agent.sh，或調整 AGENT_BUILD_SCRIPT。", scriptPath)
			return capability
		}
		capability.Reason = fmt.Sprintf("無法讀取 build script %s：%v", scriptPath, err)
		return capability
	}
	if scriptInfo.IsDir() {
		capability.Available = false
		capability.Reason = fmt.Sprintf("AGENT_BUILD_SCRIPT 指到目錄而不是檔案：%s", scriptPath)
		return capability
	}

	if _, err := exec.LookPath("bash"); err != nil {
		capability.Available = false
		capability.Reason = "找不到 bash，無法執行 build script"
		return capability
	}

	capability.Script = scriptPath
	return capability
}

func (h *releaseHandler) installCommand(r *http.Request, version string) string {
	base := baseURL(r)
	tarName := fmt.Sprintf("backup-agent_%s_linux_amd64.tar.gz", version)
	return fmt.Sprintf(
		"curl -fsSLO %s/api/admin/agent-releases/%s/download/%s && tar -xzf %s && cd backup-agent_%s_linux_amd64 && sudo AGENT_BINARY_SRC=./backup-agent-linux-amd64 ./install-agent.sh",
		base, version, tarName, tarName, version,
	)
}

func (h *releaseHandler) readManifest(version string) (*agentReleaseManifest, error) {
	path := filepath.Join(h.rootDir, version, "manifest.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var manifest agentReleaseManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return nil, err
	}
	return &manifest, nil
}

func runBuildScript(ctx context.Context, workdir, script, version string, logger *log.Logger) error {
	cmd := exec.CommandContext(ctx, "bash", script)
	cmd.Dir = workdir
	cmd.Env = append(os.Environ(), "AGENT_VERSION="+version)
	output, err := cmd.CombinedOutput()
	if logger != nil {
		if trimmed := strings.TrimSpace(string(output)); trimmed != "" {
			logger.Printf("build script output begin\n%s\nbuild script output end", trimmed)
		} else {
			logger.Printf("build script output empty")
		}
	}
	if err != nil {
		return fmt.Errorf("build agent 失敗: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func newAgentReleaseLogger(version string) (string, *log.Logger, func(), error) {
	logDir := os.Getenv("AGENT_RELEASE_LOG_DIR")
	if logDir == "" {
		logDir = "/var/log/backup-agent/releases"
	}
	runID := sanitizeReleaseToken(version) + "-" + time.Now().Format("20060102-150405")
	path, file, err := createReleaseLogFile(logDir, runID)
	if err != nil {
		fallbackDir := filepath.Join(os.TempDir(), "backup-agent", "releases")
		path, file, err = createReleaseLogFile(fallbackDir, runID)
		if err != nil {
			return "", nil, nil, err
		}
	}
	logger := log.New(file, "", log.LstdFlags|log.Lmicroseconds)
	closeFn := func() {
		_ = file.Close()
	}
	return path, logger, closeFn, nil
}

func createReleaseLogFile(logDir, runID string) (string, *os.File, error) {
	if err := os.MkdirAll(logDir, 0755); err != nil {
		return "", nil, err
	}
	path := filepath.Join(logDir, runID+".log")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return "", nil, err
	}
	return path, file, nil
}

func sanitizeReleaseToken(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "release"
	}
	value = strings.ReplaceAll(value, "/", "-")
	value = strings.ReplaceAll(value, "\\", "-")
	return value
}

func copyFile(src, dst string, mode fs.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return nil
}

func writeTarGz(destPath, dir string, files []string) error {
	out, err := os.Create(destPath)
	if err != nil {
		return err
	}
	defer out.Close()

	gw := gzip.NewWriter(out)
	defer gw.Close()
	tw := tar.NewWriter(gw)
	defer tw.Close()

	rootName := strings.TrimSuffix(filepath.Base(destPath), ".tar.gz")
	for _, name := range files {
		fullPath := filepath.Join(dir, name)
		info, err := os.Stat(fullPath)
		if err != nil {
			return err
		}
		hdr, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		hdr.Name = filepath.ToSlash(filepath.Join(rootName, name))
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		file, err := os.Open(fullPath)
		if err != nil {
			return err
		}
		if _, err := io.Copy(tw, file); err != nil {
			file.Close()
			return err
		}
		file.Close()
	}
	return nil
}

func buildReleaseFiles(dir string, files []agentReleaseFile) ([]agentReleaseFile, error) {
	out := make([]agentReleaseFile, 0, len(files))
	for _, file := range files {
		hash, size, err := sha256File(filepath.Join(dir, file.Name))
		if err != nil {
			return nil, err
		}
		file.SHA256 = hash
		file.SizeBytes = size
		out = append(out, file)
	}
	return out, nil
}

func sha256File(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()

	hash := sha256.New()
	n, err := io.Copy(hash, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(hash.Sum(nil)), n, nil
}

func extractReleaseBundle(destDir, version string, src io.Reader, logger *log.Logger) error {
	gr, err := gzip.NewReader(src)
	if err != nil {
		return fmt.Errorf("讀取 bundle gzip 失敗: %w", err)
	}
	defer gr.Close()

	tr := tar.NewReader(gr)
	prefix := version + "/"
	extracted := 0
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("讀取 bundle tar 失敗: %w", err)
		}

		name := filepath.ToSlash(strings.TrimSpace(hdr.Name))
		if name == "" || name == "." {
			continue
		}
		if name == version || name == version+"/" {
			continue
		}
		if !strings.HasPrefix(name, prefix) {
			return fmt.Errorf("bundle 內容必須位於 %s/ 目錄下: %s", version, name)
		}

		rel := strings.TrimPrefix(name, prefix)
		if rel == "" {
			continue
		}
		cleanRel := filepath.Clean(rel)
		if cleanRel == "." || cleanRel == "" {
			continue
		}
		if cleanRel == ".." || strings.HasPrefix(cleanRel, ".."+string(filepath.Separator)) || filepath.IsAbs(cleanRel) {
			return fmt.Errorf("bundle 內含非法路徑: %s", hdr.Name)
		}

		targetPath := filepath.Join(destDir, cleanRel)
		if !strings.HasPrefix(targetPath, destDir+string(filepath.Separator)) && targetPath != destDir {
			return fmt.Errorf("bundle 路徑超出目標目錄: %s", hdr.Name)
		}

		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(targetPath, 0755); err != nil {
				return fmt.Errorf("建立目錄失敗 %s: %w", cleanRel, err)
			}
		case tar.TypeReg, tar.TypeRegA:
			if err := os.MkdirAll(filepath.Dir(targetPath), 0755); err != nil {
				return fmt.Errorf("建立檔案目錄失敗 %s: %w", cleanRel, err)
			}
			mode := fs.FileMode(hdr.Mode)
			if mode == 0 {
				mode = 0644
			}
			f, err := os.OpenFile(targetPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
			if err != nil {
				return fmt.Errorf("建立檔案失敗 %s: %w", cleanRel, err)
			}
			if _, err := io.Copy(f, tr); err != nil {
				f.Close()
				return fmt.Errorf("寫入檔案失敗 %s: %w", cleanRel, err)
			}
			if err := f.Close(); err != nil {
				return fmt.Errorf("關閉檔案失敗 %s: %w", cleanRel, err)
			}
			extracted++
		default:
			return fmt.Errorf("bundle 含不支援的 tar 類型: %s", hdr.Name)
		}
	}

	if extracted == 0 {
		return fmt.Errorf("bundle 內沒有可匯入檔案")
	}
	if logger != nil {
		logger.Printf("release import extracted version=%s files=%d dir=%s", version, extracted, destDir)
	}
	return nil
}

func validateImportedRelease(releaseDir, version, logPath string) (*agentReleaseManifest, error) {
	requiredFiles := []string{
		"backup-agent-linux-amd64",
		fmt.Sprintf("backup-agent_%s_linux_amd64.tar.gz", version),
		fmt.Sprintf("backup-agent_%s_checksums.txt", version),
		"install-agent.sh",
		"diagnose-agent.sh",
		"backup-agent.service",
		"manifest.json",
	}
	for _, name := range requiredFiles {
		path := filepath.Join(releaseDir, name)
		info, err := os.Stat(path)
		if err != nil {
			if os.IsNotExist(err) {
				return nil, fmt.Errorf("release 缺少必要檔案: %s", name)
			}
			return nil, fmt.Errorf("檢查 release 檔案失敗 %s: %w", name, err)
		}
		if info.IsDir() {
			return nil, fmt.Errorf("release 檔案不可為目錄: %s", name)
		}
	}

	raw, err := os.ReadFile(filepath.Join(releaseDir, "manifest.json"))
	if err != nil {
		return nil, fmt.Errorf("讀取 manifest 失敗: %w", err)
	}
	var manifest agentReleaseManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return nil, fmt.Errorf("manifest 格式錯誤: %w", err)
	}
	if manifest.Version != version {
		return nil, fmt.Errorf("manifest version 不符: expected %s got %s", version, manifest.Version)
	}

	requiredByManifest := map[string]struct{}{}
	for _, file := range manifest.Files {
		requiredByManifest[file.Name] = struct{}{}
	}
	for _, name := range requiredFiles[:len(requiredFiles)-1] {
		if _, ok := requiredByManifest[name]; !ok {
			return nil, fmt.Errorf("manifest 缺少檔案項目: %s", name)
		}
	}

	if logPath != "" && strings.TrimSpace(manifest.LogRef) == "" {
		manifest.LogRef = logPath
		if err := writeJSONFile(filepath.Join(releaseDir, "manifest.json"), manifest); err != nil {
			return nil, fmt.Errorf("回寫 manifest log_ref 失敗: %w", err)
		}
	}
	return &manifest, nil
}

func writeJSONFile(path string, v any) error {
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	return os.WriteFile(path, raw, 0644)
}

func writeChecksums(path string, files []agentReleaseFile) error {
	var b strings.Builder
	for _, file := range files {
		b.WriteString(file.SHA256)
		b.WriteString("  ")
		b.WriteString(file.Name)
		b.WriteByte('\n')
	}
	return os.WriteFile(path, []byte(b.String()), 0644)
}

func gitCommit(ctx context.Context, workdir string) string {
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "--short", "HEAD")
	cmd.Dir = workdir
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func (h *releaseHandler) resolveBuilderAgent(ctx context.Context) (*store.Agent, error) {
	if h.store == nil {
		return nil, fmt.Errorf("build agent store 未設定")
	}

	preferredCode := strings.TrimSpace(os.Getenv("AGENT_RELEASE_BUILDER_CODE"))
	if preferredCode != "" {
		agent, err := h.store.GetAgentByCode(ctx, preferredCode)
		if err != nil {
			return nil, fmt.Errorf("找不到指定的 build agent: %s", preferredCode)
		}
		if !agent.Enabled {
			return nil, fmt.Errorf("指定的 build agent %s 未啟用", preferredCode)
		}
		if !h.store.IsAgentOnline(agent.LastSeenAt) {
			return nil, fmt.Errorf("指定的 build agent %s 目前離線", preferredCode)
		}
		return agent, nil
	}

	agents, err := h.store.ListAgents(ctx)
	if err != nil {
		return nil, fmt.Errorf("列出 agent 失敗: %w", err)
	}

	candidates := make([]store.Agent, 0, len(agents))
	for _, agent := range agents {
		if !agent.Enabled {
			continue
		}
		if !h.store.IsAgentOnline(agent.LastSeenAt) {
			continue
		}
		candidates = append(candidates, agent)
	}

	switch len(candidates) {
	case 0:
		return nil, fmt.Errorf("找不到可用的 build agent；請確認至少一台 agent online，或設定 AGENT_RELEASE_BUILDER_CODE")
	case 1:
		return &candidates[0], nil
	default:
		names := make([]string, 0, len(candidates))
		for _, agent := range candidates {
			names = append(names, agent.Code)
		}
		return nil, fmt.Errorf("目前有多台可用 build agent: %s；請設定 AGENT_RELEASE_BUILDER_CODE", strings.Join(names, ", "))
	}
}

func fetchAgentReleaseCapability(ctx context.Context, agent *store.Agent) (agentReleaseCapability, error) {
	var cap agentReleaseCapability
	err := doAgentReleaseJSON(ctx, agent, http.MethodGet, "/releases/capability", nil, &cap)
	return cap, err
}

func triggerAgentReleaseBuild(ctx context.Context, agent *store.Agent, version string) (*agentReleaseDetail, error) {
	var detail agentReleaseDetail
	if err := doAgentReleaseJSON(ctx, agent, http.MethodPost, "/releases/build", map[string]string{"version": version}, &detail); err != nil {
		return nil, err
	}
	return &detail, nil
}

func doAgentReleaseJSON(ctx context.Context, agent *store.Agent, method, path string, body any, out any) error {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(agent.BaseURL, "/")+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Agent-Code", agent.Code)
	req.Header.Set("X-Agent-Token", agent.TokenHash)

	resp, err := (&http.Client{Timeout: 5 * time.Minute}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		var body struct {
			Error  string `json:"error"`
			LogRef string `json:"log_ref"`
		}
		raw, _ := io.ReadAll(resp.Body)
		_ = json.Unmarshal(raw, &body)
		msg := strings.TrimSpace(body.Error)
		if msg == "" {
			msg = strings.TrimSpace(string(raw))
		}
		if body.LogRef != "" {
			msg = strings.TrimSpace(msg + " (log: " + body.LogRef + ")")
		}
		return fmt.Errorf("agent 回應 %d: %s", resp.StatusCode, msg)
	}

	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

func downloadAgentReleaseFile(ctx context.Context, agent *store.Agent, version, fileName, dst string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(agent.BaseURL, "/")+fmt.Sprintf("/releases/%s/download/%s", version, fileName), nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-Agent-Code", agent.Code)
	req.Header.Set("X-Agent-Token", agent.TokenHash)

	resp, err := (&http.Client{Timeout: 5 * time.Minute}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		raw, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("agent 回應 %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, resp.Body)
	return err
}

func baseURL(r *http.Request) string {
	if publicBase := strings.TrimSpace(os.Getenv("DASHBOARD_PUBLIC_URL")); publicBase != "" {
		return strings.TrimRight(publicBase, "/")
	}
	if publicBase := strings.TrimSpace(os.Getenv("PUBLIC_BASE_URL")); publicBase != "" {
		return strings.TrimRight(publicBase, "/")
	}
	if r == nil {
		return ""
	}

	scheme := "http"
	if proto := strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")); proto != "" {
		scheme = strings.Split(proto, ",")[0]
	} else if r.TLS != nil {
		scheme = "https"
	}

	host := strings.TrimSpace(r.Header.Get("X-Forwarded-Host"))
	if host == "" {
		host = r.Host
	} else {
		host = strings.Split(host, ",")[0]
	}
	return scheme + "://" + strings.TrimSpace(host)
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func acquireAgentReleaseBuildSlot(ctx context.Context, logger *log.Logger, version string) (func(), error) {
	select {
	case agentReleaseBuildLimiter <- struct{}{}:
		if logger != nil {
			logger.Printf("acquired build slot version=%s", version)
		}
		return func() {
			<-agentReleaseBuildLimiter
			if logger != nil {
				logger.Printf("released build slot version=%s", version)
			}
		}, nil
	case <-ctx.Done():
		return nil, fmt.Errorf("等待前一個 build 完成時請求已取消: %w", ctx.Err())
	default:
		if logger != nil {
			logger.Printf("build slot busy; waiting version=%s", version)
		}
	}

	select {
	case agentReleaseBuildLimiter <- struct{}{}:
		if logger != nil {
			logger.Printf("acquired build slot after wait version=%s", version)
		}
		return func() {
			<-agentReleaseBuildLimiter
			if logger != nil {
				logger.Printf("released build slot version=%s", version)
			}
		}, nil
	case <-ctx.Done():
		return nil, fmt.Errorf("等待前一個 build 完成時請求已取消: %w", ctx.Err())
	}
}
