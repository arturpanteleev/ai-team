package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/logging"
	"github.com/arturpanteleev/ai-team/pkg/safeio"
)

const stageWebTokenEnv = "AI_TEAM_WEB_TOKEN"

type stageTakeOwner struct {
	StageID    string    `json:"stage_id"`
	ApprovalID string    `json:"approval_id"`
	ActorID    string    `json:"actor_id"`
	ActorRole  string    `json:"actor_role"`
	TakenAt    time.Time `json:"taken_at"`
}

type stageTakeResponse struct {
	Owner   stageTakeOwner `json:"owner"`
	Changed bool           `json:"changed"`
}

type stageSessionResponse struct {
	CSRFToken string `json:"csrf_token"`
}

func cmdTaskStageTake(args []string) {
	flags := flag.NewFlagSet("stage take", flag.ExitOnError)
	target := flags.String("target", ".", "Путь к целевому проекту")
	stageID := flags.String("stage", "", "ID human stage")
	webURL := flags.String("web-url", os.Getenv("AI_TEAM_WEB_URL"), "URL web controller; по умолчанию http://127.0.0.1:8080")
	valueFlags := map[string]bool{"--target": true, "--stage": true, "--web-url": true}
	if err := flags.Parse(interspersedTaskArgs(args, valueFlags)); err != nil {
		fatal("Ошибка аргументов stage take: %v", err)
	}
	stageName := strings.TrimSpace(*stageID)
	if flags.NArg() != 1 || stageName == "" {
		fatal("Использование: ai-team stage take <run_id> --stage <id> [--target <dir>] [--web-url <url>]")
	}
	runID := strings.TrimSpace(flags.Arg(0))
	if !safeStageCLIIdentity(runID) || !safeStageCLIIdentity(stageName) {
		fatal("run_id и stage id должны быть простыми идентификаторами без разделителей пути")
	}
	targetDir, err := absoluteTarget(*target)
	if err != nil {
		fatal("Ошибка target: %v", err)
	}
	requireControlRoot(targetDir)

	baseURL, origin, loopback, err := parseStageWebURL(*webURL)
	if err != nil {
		fatal("Ошибка web URL: %v", err)
	}
	token := strings.TrimSpace(os.Getenv(stageWebTokenEnv))
	if token == "" && loopback {
		token, _, err = readLocalWebToken(targetDir)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			fatal("Не удалось прочитать локальный web token: %v", err)
		}
	}
	if token == "" {
		fatal("для stage take нужен bearer token: локально он берётся из .ai-team/web.token, для облака задайте %s", stageWebTokenEnv)
	}

	jar, err := cookiejar.New(nil)
	if err != nil {
		fatal("Не удалось подготовить web session: %v", err)
	}
	client := &http.Client{
		Timeout: 15 * time.Second,
		Jar:     jar,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	result, err := takeStageOverWeb(context.Background(), client, baseURL, origin, token, runID, stageName)
	if err != nil {
		logging.Fail(logging.Record{Level: "error", Command: "stage take", Type: "stage_action", Message: err.Error(), Exit: exitFailed}, "Не удалось взять этап: %v", err)
		os.Exit(exitFailed)
	}
	message := fmt.Sprintf("Этап %s задачи %s взят участником %s (%s)", result.Owner.StageID, runID, result.Owner.ActorID, result.Owner.ActorRole)
	if !result.Changed {
		message = fmt.Sprintf("Этап %s задачи %s уже закреплён за участником %s (%s)", result.Owner.StageID, runID, result.Owner.ActorID, result.Owner.ActorRole)
	}
	if logging.GetMode() != logging.ModeJSON && logging.GetMode() != logging.ModeQuiet {
		logging.Printf("%s\n", message)
	}
	logging.Emit(logging.Record{Level: "ok", Command: "stage take", Type: "stage_owner", Message: message,
		Data: map[string]any{"run_id": runID, "stage_id": stageName, "owner": result.Owner, "changed": result.Changed}, Exit: exitOK})
}

func safeStageCLIIdentity(value string) bool {
	return value != "" && filepath.Base(value) == value && value != "." && value != ".." && !strings.ContainsAny(value, `/\\`)
}

func parseStageWebURL(raw string) (baseURL, origin string, loopback bool, err error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		raw = "http://127.0.0.1:8080"
	}
	parsed, parseErr := url.Parse(raw)
	if parseErr != nil || parsed.Opaque != "" || parsed.User != nil || parsed.Host == "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", "", false, errors.New("ожидался URL вида https://host[:port] без userinfo, query и fragment")
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", "", false, errors.New("поддерживаются только http и https")
	}
	host := strings.ToLower(strings.TrimSuffix(parsed.Hostname(), "."))
	ip := net.ParseIP(host)
	loopback = strings.EqualFold(host, "localhost") || ip != nil && ip.IsLoopback()
	if parsed.Scheme != "https" && !loopback {
		return "", "", false, errors.New("удалённый web controller требует HTTPS")
	}
	if parsed.Path == "/" {
		parsed.Path = ""
	}
	parsed.RawPath = ""
	baseURL = strings.TrimRight(parsed.String(), "/")
	origin = parsed.Scheme + "://" + parsed.Host
	return baseURL, origin, loopback, nil
}

func readLocalWebToken(target string) (string, string, error) {
	controlDir, err := safeio.ExistingDir(target, ".ai-team")
	if err != nil {
		return "", "", err
	}
	path := filepath.Join(controlDir, "web.token")
	info, err := os.Lstat(path)
	if err != nil {
		return "", "", err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return "", "", errors.New("web.token must be a regular file without symlink")
	}
	if info.Mode().Perm() != 0o600 {
		return "", "", fmt.Errorf("web.token permissions are %04o; expected 0600", info.Mode().Perm())
	}
	data, err := safeio.ReadRegularFile(path, 1024)
	if err != nil {
		return "", "", err
	}
	token := strings.TrimSuffix(string(data), "\n")
	if err := validateLocalWebToken(token); err != nil {
		return "", "", fmt.Errorf("invalid web.token: %w", err)
	}
	return token, path, nil
}

func takeStageOverWeb(ctx context.Context, client *http.Client, baseURL, origin, token, runID, stageID string) (stageTakeResponse, error) {
	if !safeStageCLIIdentity(runID) || !safeStageCLIIdentity(stageID) {
		return stageTakeResponse{}, errors.New("invalid run or stage id")
	}
	sessionRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/api/session", nil)
	if err != nil {
		return stageTakeResponse{}, err
	}
	sessionRequest.Header.Set("Authorization", "Bearer "+token)
	sessionRequest.Header.Set("Origin", origin)
	sessionResponse, err := client.Do(sessionRequest)
	if err != nil {
		return stageTakeResponse{}, fmt.Errorf("создание web session: %w", err)
	}
	var session stageSessionResponse
	decodeErr := decodeStageResponse(sessionResponse, &session)
	if decodeErr != nil {
		return stageTakeResponse{}, fmt.Errorf("создание web session: %w", decodeErr)
	}
	if strings.TrimSpace(session.CSRFToken) == "" {
		return stageTakeResponse{}, errors.New("web controller не выдал CSRF token")
	}
	endpoint := baseURL + "/api/runs/" + url.PathEscape(runID) + "/stages/" + url.PathEscape(stageID) + "/take"
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, nil)
	if err != nil {
		return stageTakeResponse{}, err
	}
	request.Header.Set("Origin", origin)
	request.Header.Set("X-CSRF-Token", session.CSRFToken)
	response, err := client.Do(request)
	if err != nil {
		return stageTakeResponse{}, fmt.Errorf("запрос stage take: %w", err)
	}
	var result stageTakeResponse
	if err := decodeStageResponse(response, &result); err != nil {
		return stageTakeResponse{}, fmt.Errorf("запрос stage take: %w", err)
	}
	if result.Owner.StageID != stageID || result.Owner.ActorID == "" || result.Owner.ActorRole == "" || result.Owner.ApprovalID == "" {
		return stageTakeResponse{}, errors.New("web controller вернул неполные данные владельца этапа")
	}
	return result, nil
}

func decodeStageResponse(response *http.Response, target any) error {
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		message, _ := io.ReadAll(io.LimitReader(response.Body, 1024))
		return fmt.Errorf("HTTP %d: %s", response.StatusCode, strings.TrimSpace(string(message)))
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, 64<<10))
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("invalid response: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("response contains trailing data")
	}
	return nil
}
