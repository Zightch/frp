package api

const indexHTML = `<!DOCTYPE html>
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
