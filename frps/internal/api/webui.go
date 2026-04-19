package api

import (
	"fmt"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
)

const placeholderIndexHTML = `<!DOCTYPE html>
<html lang="zh-CN">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>frps Management</title>
  <style>
    :root {
      color-scheme: light;
      --bg: #f1eee7;
      --card: rgba(255, 253, 249, 0.92);
      --ink: #1d2733;
      --muted: #687180;
      --line: #d9cfbf;
      --accent: #27584f;
      --accent-soft: #e7f0ec;
    }
    * { box-sizing: border-box; }
    body {
      margin: 0;
      min-height: 100vh;
      display: grid;
      place-items: center;
      padding: 24px;
      font-family: "Segoe UI", "PingFang SC", sans-serif;
      color: var(--ink);
      background:
        radial-gradient(circle at top left, #efe2c9, transparent 32%),
        linear-gradient(180deg, #f7f3ea 0%, var(--bg) 100%);
    }
    main {
      width: min(720px, 100%);
      padding: 28px;
      border: 1px solid var(--line);
      border-radius: 24px;
      background: var(--card);
      box-shadow: 0 24px 48px rgba(29, 39, 51, 0.08);
    }
    h1 {
      margin: 0;
      font-size: 32px;
    }
    p {
      margin: 0;
      line-height: 1.6;
      color: var(--muted);
    }
    .stack {
      display: grid;
      gap: 14px;
    }
    .hint {
      padding: 14px 16px;
      border-radius: 16px;
      background: var(--accent-soft);
      color: var(--accent);
    }
    code {
      font-family: Consolas, "Courier New", monospace;
      background: rgba(39, 88, 79, 0.08);
      padding: 2px 6px;
      border-radius: 8px;
    }
  </style>
</head>
<body>
  <main class="stack">
    <h1>frps 管理面改造中</h1>
    <p>当前阶段已切换到本地 <code>auth.json</code> 管理密钥方案。完整 WebUI 将在后续阶段迁移为独立的 Node.js + Vue 3 + Element Plus 工程。</p>
    <div class="hint">
      <strong>当前可用最小认证 API</strong>
      <p><code>GET /api/v1/auth/state</code> 查看是否已初始化。</p>
      <p><code>POST /api/v1/auth/init</code> 在未初始化时写入管理密钥 hash。</p>
      <p><code>POST /api/v1/auth/challenge</code> 在已初始化后申请一次性盐 challenge。</p>
      <p><code>POST /api/v1/auth/login</code> 提交 <code>challenge_id</code> 和 <code>proof</code>，由服务端签发管理会话。</p>
      <p><code>GET /api/v1/auth/session</code> 查看当前管理会话状态；<code>POST /api/v1/auth/logout</code> 注销当前会话。</p>
    </div>
    <p>当前业务管理接口已经要求有效管理会话，后续阶段再切换到独立的 Node.js + Vue 3 + Element Plus WebUI。</p>
  </main>
</body>
</html>
`

func newWebUIHandler(distDir string) (http.Handler, error) {
	distDir = strings.TrimSpace(distDir)
	if distDir == "" {
		return http.HandlerFunc(handlePlaceholderIndex), nil
	}

	info, err := os.Stat(distDir)
	if err != nil {
		return nil, fmt.Errorf("stat webui.dist_dir %q: %w", distDir, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("webui.dist_dir %q must be a directory", distDir)
	}

	indexPath := filepath.Join(distDir, "index.html")
	indexInfo, err := os.Stat(indexPath)
	if err != nil {
		return nil, fmt.Errorf("stat webui index %q: %w", indexPath, err)
	}
	if indexInfo.IsDir() {
		return nil, fmt.Errorf("webui index %q must be a file", indexPath)
	}

	return &webUIHandler{
		distDir:    distDir,
		indexPath:  indexPath,
		fileServer: http.FileServer(http.Dir(distDir)),
	}, nil
}

type webUIHandler struct {
	distDir    string
	indexPath  string
	fileServer http.Handler
}

func (h *webUIHandler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet && request.Method != http.MethodHead {
		http.NotFound(writer, request)
		return
	}

	if request.URL.Path == "/" {
		h.serveIndex(writer, request)
		return
	}

	cleanPath := path.Clean("/" + request.URL.Path)
	relativePath := strings.TrimPrefix(cleanPath, "/")
	if relativePath == "" || relativePath == "." {
		h.serveIndex(writer, request)
		return
	}

	targetPath := filepath.Join(h.distDir, filepath.FromSlash(relativePath))
	info, err := os.Stat(targetPath)
	switch {
	case err == nil && info.IsDir():
		http.NotFound(writer, request)
		return
	case err == nil:
		h.fileServer.ServeHTTP(writer, request)
		return
	case !os.IsNotExist(err):
		http.Error(writer, "webui asset lookup failed", http.StatusInternalServerError)
		return
	case path.Ext(cleanPath) != "":
		http.NotFound(writer, request)
		return
	default:
		h.serveIndex(writer, request)
	}
}

func (h *webUIHandler) serveIndex(writer http.ResponseWriter, request *http.Request) {
	http.ServeFile(writer, request, h.indexPath)
}

func handlePlaceholderIndex(writer http.ResponseWriter, request *http.Request) {
	if request.URL.Path != "/" {
		http.NotFound(writer, request)
		return
	}

	writer.Header().Set("Content-Type", "text/html; charset=utf-8")
	writer.WriteHeader(http.StatusOK)
	_, _ = writer.Write([]byte(placeholderIndexHTML))
}
