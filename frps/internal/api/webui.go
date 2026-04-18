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
      --bg: #f4f1ea;
      --card: #fffdf8;
      --ink: #1f2933;
      --line: #d8cfc0;
      --accent: #205b4a;
      --accent-soft: #e4efe9;
      --danger: #8f2d2d;
      --muted: #6b7280;
    }
    * { box-sizing: border-box; }
    body {
      margin: 0;
      font-family: "Segoe UI", "PingFang SC", sans-serif;
      color: var(--ink);
      background:
        radial-gradient(circle at top left, #efe5d3, transparent 28%),
        linear-gradient(180deg, #f7f3eb 0%, var(--bg) 100%);
    }
    main {
      max-width: 1200px;
      margin: 0 auto;
      padding: 24px 16px 40px;
    }
    h1, h2, h3 { margin: 0; }
    p { margin: 0; }
    .hero {
      padding: 18px 20px;
      border: 1px solid var(--line);
      border-radius: 18px;
      background: rgba(255, 253, 248, 0.9);
      backdrop-filter: blur(8px);
      box-shadow: 0 18px 36px rgba(31, 41, 51, 0.06);
    }
    .hero p {
      margin-top: 8px;
      color: var(--muted);
      line-height: 1.5;
    }
    .grid {
      display: grid;
      grid-template-columns: repeat(auto-fit, minmax(320px, 1fr));
      gap: 16px;
      margin-top: 16px;
    }
    .card {
      border: 1px solid var(--line);
      border-radius: 18px;
      background: var(--card);
      padding: 16px;
      box-shadow: 0 12px 28px rgba(31, 41, 51, 0.05);
    }
    .card h2 {
      font-size: 18px;
      margin-bottom: 12px;
    }
    .stack {
      display: grid;
      gap: 10px;
    }
    label {
      display: grid;
      gap: 6px;
      font-size: 14px;
      color: var(--muted);
    }
    input, select, button {
      font: inherit;
    }
    input, select {
      width: 100%;
      padding: 10px 12px;
      border: 1px solid var(--line);
      border-radius: 10px;
      background: #fff;
      color: var(--ink);
    }
    input[type="checkbox"] {
      width: auto;
      margin-right: 8px;
    }
    .inline {
      display: flex;
      align-items: center;
      gap: 8px;
      color: var(--ink);
    }
    .actions {
      display: flex;
      gap: 8px;
      flex-wrap: wrap;
    }
    button {
      border: 0;
      border-radius: 10px;
      padding: 10px 14px;
      cursor: pointer;
      background: var(--accent);
      color: #fff;
    }
    button.secondary {
      background: var(--accent-soft);
      color: var(--accent);
    }
    button.danger {
      background: #f9e4e4;
      color: var(--danger);
    }
    .table-wrap {
      overflow-x: auto;
      margin-top: 12px;
    }
    table {
      width: 100%;
      border-collapse: collapse;
      font-size: 14px;
    }
    th, td {
      text-align: left;
      padding: 10px 8px;
      border-bottom: 1px solid #ece5d8;
      vertical-align: top;
    }
    th {
      color: var(--muted);
      font-weight: 600;
    }
    .mono {
      font-family: Consolas, "Courier New", monospace;
      word-break: break-all;
    }
    .status {
      margin-top: 16px;
      padding: 12px 14px;
      border-radius: 12px;
      border: 1px solid var(--line);
      background: rgba(255, 253, 248, 0.9);
      min-height: 46px;
      line-height: 1.5;
    }
    .status.error {
      border-color: #efb7b7;
      background: #fff0f0;
      color: var(--danger);
    }
    .token-box {
      margin-top: 10px;
      padding: 12px 14px;
      border-radius: 12px;
      border: 1px dashed #8fb3a6;
      background: #eef7f3;
    }
    .token-box strong {
      display: block;
      margin-bottom: 6px;
    }
    .token-box.hidden {
      display: none;
    }
    @media (max-width: 720px) {
      .actions { flex-direction: column; }
      button { width: 100%; }
    }
  </style>
</head>
<body>
  <main>
    <section class="hero">
      <h1>frps 极简管理页</h1>
      <p>当前页面只管理 proxy_groups 与 tunnels。数据库写入全部通过 frps management api 完成，token 明文只在创建或重置时返回一次。每个分组固定只有一个客户端槽位；槽位被占用时新的 frpc 登录会被拒绝。</p>
    </section>

    <section class="grid">
      <article class="card">
        <h2>分组管理</h2>
        <form id="group-form" class="stack">
          <input id="group-id" type="hidden">
          <label>名称
            <input id="group-name" name="name" required maxlength="128">
          </label>
          <label class="inline">
            <input id="group-enabled" name="enabled" type="checkbox" checked>
            启用
          </label>
          <div class="actions">
            <button type="submit">保存分组</button>
            <button type="button" id="group-reset" class="secondary">清空表单</button>
          </div>
        </form>
        <div id="token-box" class="token-box hidden">
          <strong>新 token</strong>
          <div id="token-value" class="mono"></div>
        </div>
        <div class="table-wrap">
          <table>
            <thead>
              <tr>
                <th>ID</th>
                <th>名称</th>
                <th>Token ID</th>
                <th>启用</th>
                <th>操作</th>
              </tr>
            </thead>
            <tbody id="group-table"></tbody>
          </table>
        </div>
      </article>

      <article class="card">
        <h2>隧道管理</h2>
        <form id="tunnel-form" class="stack">
          <input id="tunnel-id" type="hidden">
          <label>所属分组
            <select id="tunnel-group-id" name="group_id" required></select>
          </label>
          <label>名称
            <input id="tunnel-name" name="name" required maxlength="128">
          </label>
          <label>协议
            <select id="tunnel-protocol" name="protocol">
              <option value="tcp">tcp</option>
              <option value="udp">udp</option>
            </select>
          </label>
          <label>映射类型
            <select id="tunnel-remote-type" name="remote_type">
              <option value="single">single</option>
              <option value="range">range</option>
            </select>
          </label>
          <label>远端起始端口
            <input id="tunnel-remote-start" name="remote_start" type="number" min="1" max="65535" value="20000" required>
          </label>
          <label>远端结束端口
            <input id="tunnel-remote-end" name="remote_end" type="number" min="1" max="65535" value="20000" required>
          </label>
          <label>内网主机
            <input id="tunnel-local-host" name="local_host" value="127.0.0.1" required>
          </label>
          <label>内网起始端口
            <input id="tunnel-local-start" name="local_start" type="number" min="1" max="65535" value="8080" required>
          </label>
          <label>内网结束端口
            <input id="tunnel-local-end" name="local_end" type="number" min="1" max="65535" value="8080" required>
          </label>
          <label class="inline">
            <input id="tunnel-enabled" name="enabled" type="checkbox" checked>
            启用
          </label>
          <div class="actions">
            <button type="submit">保存隧道</button>
            <button type="button" id="tunnel-reset" class="secondary">清空表单</button>
          </div>
        </form>
        <div class="table-wrap">
          <table>
            <thead>
              <tr>
                <th>ID</th>
                <th>分组</th>
                <th>名称</th>
                <th>协议</th>
                <th>远端</th>
                <th>内网</th>
                <th>启用</th>
                <th>操作</th>
              </tr>
            </thead>
            <tbody id="tunnel-table"></tbody>
          </table>
        </div>
      </article>
    </section>

    <div id="status" class="status">页面已加载，正在拉取数据。</div>
  </main>

  <script>
    const state = {
      groups: [],
      tunnels: []
    };

    const groupForm = document.getElementById("group-form");
    const tunnelForm = document.getElementById("tunnel-form");
    const statusBox = document.getElementById("status");
    const tokenBox = document.getElementById("token-box");
    const tokenValue = document.getElementById("token-value");
    const groupTable = document.getElementById("group-table");
    const tunnelTable = document.getElementById("tunnel-table");
    const tunnelGroupSelect = document.getElementById("tunnel-group-id");

    function setStatus(message, isError) {
      statusBox.textContent = message;
      statusBox.className = isError ? "status error" : "status";
    }

    function showToken(token) {
      if (!token) {
        tokenBox.classList.add("hidden");
        tokenValue.textContent = "";
        return;
      }
      tokenBox.classList.remove("hidden");
      tokenValue.textContent = token;
    }

    async function requestJSON(url, options) {
      const response = await fetch(url, Object.assign({
        headers: { "Content-Type": "application/json" }
      }, options || {}));
      let payload = {};
      try {
        payload = await response.json();
      } catch (error) {
        payload = {};
      }
      if (!response.ok) {
        throw new Error(payload.error || ("HTTP " + response.status));
      }
      return payload;
    }

    function renderGroups() {
      groupTable.innerHTML = "";
      tunnelGroupSelect.innerHTML = "";

      if (state.groups.length === 0) {
        const row = document.createElement("tr");
        row.innerHTML = "<td colspan='5'>暂无分组</td>";
        groupTable.appendChild(row);

        const option = document.createElement("option");
        option.value = "";
        option.textContent = "请先创建分组";
        tunnelGroupSelect.appendChild(option);
        return;
      }

      state.groups.forEach(function (item) {
        const option = document.createElement("option");
        option.value = String(item.id);
        option.textContent = item.name + " (#" + item.id + ")";
        tunnelGroupSelect.appendChild(option);

        const row = document.createElement("tr");
        const actions = document.createElement("td");

        const editButton = document.createElement("button");
        editButton.type = "button";
        editButton.className = "secondary";
        editButton.textContent = "编辑";
        editButton.addEventListener("click", function () {
          fillGroupForm(item);
        });

        const tokenButton = document.createElement("button");
        tokenButton.type = "button";
        tokenButton.className = "secondary";
        tokenButton.textContent = "重置 token";
        tokenButton.addEventListener("click", async function () {
          if (!confirm("确认重置 token 吗？")) {
            return;
          }
          try {
            const payload = await requestJSON("/api/v1/proxy-groups/" + item.id + "/token", {
              method: "POST"
            });
            await loadAll("token 已重置");
            showToken(payload.token);
          } catch (error) {
            setStatus(error.message, true);
          }
        });

        const deleteButton = document.createElement("button");
        deleteButton.type = "button";
        deleteButton.className = "danger";
        deleteButton.textContent = "删除";
        deleteButton.addEventListener("click", async function () {
          if (!confirm("删除分组会同时删除该分组下的 tunnels，继续吗？")) {
            return;
          }
          try {
            await requestJSON("/api/v1/proxy-groups/" + item.id, {
              method: "DELETE"
            });
            resetGroupForm();
            await loadAll("分组已删除");
          } catch (error) {
            setStatus(error.message, true);
          }
        });

        actions.appendChild(editButton);
        actions.appendChild(tokenButton);
        actions.appendChild(deleteButton);

        row.innerHTML =
          "<td>" + item.id + "</td>" +
          "<td>" + escapeHTML(item.name) + "</td>" +
          "<td class='mono'>" + escapeHTML(item.token_id) + "</td>" +
          "<td>" + yesNo(item.enabled) + "</td>";
        row.appendChild(actions);
        groupTable.appendChild(row);
      });
    }

    function renderTunnels() {
      tunnelTable.innerHTML = "";
      if (state.tunnels.length === 0) {
        const row = document.createElement("tr");
        row.innerHTML = "<td colspan='8'>暂无隧道</td>";
        tunnelTable.appendChild(row);
        return;
      }

      state.tunnels.forEach(function (item) {
        const row = document.createElement("tr");
        const actions = document.createElement("td");

        const editButton = document.createElement("button");
        editButton.type = "button";
        editButton.className = "secondary";
        editButton.textContent = "编辑";
        editButton.addEventListener("click", function () {
          fillTunnelForm(item);
        });

        const deleteButton = document.createElement("button");
        deleteButton.type = "button";
        deleteButton.className = "danger";
        deleteButton.textContent = "删除";
        deleteButton.addEventListener("click", async function () {
          if (!confirm("确认删除隧道吗？")) {
            return;
          }
          try {
            await requestJSON("/api/v1/tunnels/" + item.id, {
              method: "DELETE"
            });
            resetTunnelForm();
            await loadAll("隧道已删除");
          } catch (error) {
            setStatus(error.message, true);
          }
        });

        actions.appendChild(editButton);
        actions.appendChild(deleteButton);

        row.innerHTML =
          "<td>" + item.id + "</td>" +
          "<td>" + escapeHTML(item.group_name) + "</td>" +
          "<td>" + escapeHTML(item.name) + "</td>" +
          "<td>" + escapeHTML(item.protocol) + "</td>" +
          "<td>" + item.remote_start + "-" + item.remote_end + " (" + escapeHTML(item.remote_type) + ")" + "</td>" +
          "<td>" + escapeHTML(item.local_host) + ":" + item.local_start + "-" + item.local_end + "</td>" +
          "<td>" + yesNo(item.enabled) + "</td>";
        row.appendChild(actions);
        tunnelTable.appendChild(row);
      });
    }

    function fillGroupForm(item) {
      document.getElementById("group-id").value = item.id;
      document.getElementById("group-name").value = item.name;
      document.getElementById("group-enabled").checked = Boolean(item.enabled);
      showToken("");
      setStatus("正在编辑分组 #" + item.id, false);
    }

    function resetGroupForm() {
      groupForm.reset();
      document.getElementById("group-id").value = "";
      document.getElementById("group-enabled").checked = true;
      showToken("");
    }

    function fillTunnelForm(item) {
      document.getElementById("tunnel-id").value = item.id;
      document.getElementById("tunnel-group-id").value = String(item.group_id);
      document.getElementById("tunnel-name").value = item.name;
      document.getElementById("tunnel-protocol").value = item.protocol;
      document.getElementById("tunnel-remote-type").value = item.remote_type;
      document.getElementById("tunnel-remote-start").value = item.remote_start;
      document.getElementById("tunnel-remote-end").value = item.remote_end;
      document.getElementById("tunnel-local-host").value = item.local_host;
      document.getElementById("tunnel-local-start").value = item.local_start;
      document.getElementById("tunnel-local-end").value = item.local_end;
      document.getElementById("tunnel-enabled").checked = Boolean(item.enabled);
      setStatus("正在编辑隧道 #" + item.id, false);
    }

    function resetTunnelForm() {
      tunnelForm.reset();
      document.getElementById("tunnel-id").value = "";
      document.getElementById("tunnel-protocol").value = "tcp";
      document.getElementById("tunnel-remote-type").value = "single";
      document.getElementById("tunnel-remote-start").value = "20000";
      document.getElementById("tunnel-remote-end").value = "20000";
      document.getElementById("tunnel-local-host").value = "127.0.0.1";
      document.getElementById("tunnel-local-start").value = "8080";
      document.getElementById("tunnel-local-end").value = "8080";
      document.getElementById("tunnel-enabled").checked = true;
      if (state.groups.length > 0) {
        document.getElementById("tunnel-group-id").value = String(state.groups[0].id);
      }
    }

    function yesNo(value) {
      return value ? "是" : "否";
    }

    function escapeHTML(value) {
      return String(value || "")
        .replaceAll("&", "&amp;")
        .replaceAll("<", "&lt;")
        .replaceAll(">", "&gt;")
        .replaceAll("\"", "&quot;")
        .replaceAll("'", "&#39;");
    }

    async function loadAll(message) {
      const groupsPayload = await requestJSON("/api/v1/proxy-groups");
      const tunnelsPayload = await requestJSON("/api/v1/tunnels");
      state.groups = groupsPayload.items || [];
      state.tunnels = tunnelsPayload.items || [];
      renderGroups();
      renderTunnels();
      if (!document.getElementById("group-id").value) {
        resetGroupForm();
      }
      if (!document.getElementById("tunnel-id").value) {
        resetTunnelForm();
      }
      setStatus(message || "数据已刷新", false);
    }

    groupForm.addEventListener("submit", async function (event) {
      event.preventDefault();
      const id = document.getElementById("group-id").value;
      const payload = {
        name: document.getElementById("group-name").value,
        enabled: document.getElementById("group-enabled").checked
      };

      try {
        if (id) {
          await requestJSON("/api/v1/proxy-groups/" + id, {
            method: "PATCH",
            body: JSON.stringify(payload)
          });
          showToken("");
          resetGroupForm();
          await loadAll("分组已更新");
          return;
        }

        const result = await requestJSON("/api/v1/proxy-groups", {
          method: "POST",
          body: JSON.stringify(payload)
        });
        resetGroupForm();
        await loadAll("分组已创建");
        showToken(result.token);
      } catch (error) {
        setStatus(error.message, true);
      }
    });

    tunnelForm.addEventListener("submit", async function (event) {
      event.preventDefault();
      const id = document.getElementById("tunnel-id").value;
      const payload = {
        group_id: Number(document.getElementById("tunnel-group-id").value),
        name: document.getElementById("tunnel-name").value,
        protocol: document.getElementById("tunnel-protocol").value,
        remote_type: document.getElementById("tunnel-remote-type").value,
        remote_start: Number(document.getElementById("tunnel-remote-start").value),
        remote_end: Number(document.getElementById("tunnel-remote-end").value),
        local_host: document.getElementById("tunnel-local-host").value,
        local_start: Number(document.getElementById("tunnel-local-start").value),
        local_end: Number(document.getElementById("tunnel-local-end").value),
        enabled: document.getElementById("tunnel-enabled").checked
      };

      try {
        if (id) {
          await requestJSON("/api/v1/tunnels/" + id, {
            method: "PATCH",
            body: JSON.stringify(payload)
          });
          resetTunnelForm();
          await loadAll("隧道已更新");
          return;
        }

        await requestJSON("/api/v1/tunnels", {
          method: "POST",
          body: JSON.stringify(payload)
        });
        resetTunnelForm();
        await loadAll("隧道已创建");
      } catch (error) {
        setStatus(error.message, true);
      }
    });

    document.getElementById("group-reset").addEventListener("click", function () {
      resetGroupForm();
      setStatus("分组表单已清空", false);
    });

    document.getElementById("tunnel-reset").addEventListener("click", function () {
      resetTunnelForm();
      setStatus("隧道表单已清空", false);
    });

    document.getElementById("tunnel-remote-type").addEventListener("change", function (event) {
      if (event.target.value === "single") {
        document.getElementById("tunnel-remote-end").value = document.getElementById("tunnel-remote-start").value;
        document.getElementById("tunnel-local-end").value = document.getElementById("tunnel-local-start").value;
      }
    });

    document.getElementById("tunnel-remote-start").addEventListener("change", function () {
      if (document.getElementById("tunnel-remote-type").value === "single") {
        document.getElementById("tunnel-remote-end").value = document.getElementById("tunnel-remote-start").value;
      }
    });

    document.getElementById("tunnel-local-start").addEventListener("change", function () {
      if (document.getElementById("tunnel-remote-type").value === "single") {
        document.getElementById("tunnel-local-end").value = document.getElementById("tunnel-local-start").value;
      }
    });

    loadAll().catch(function (error) {
      setStatus(error.message, true);
    });
  </script>
</body>
</html>
`
