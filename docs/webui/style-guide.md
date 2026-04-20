# frps WebUI 视觉风格规范

更新时间：2026-04-20

本文档为 `frps/webui` 前端界面的统一视觉规范。所有页面、组件的视觉表达必须遵守本规范，以确保界面一致性和专业感。

参考产品：1Panel、宝塔面板、New-API、Ant Design Pro、Element Plus 默认主题。

---

## 1. 设计原则

| 原则 | 含义 |
|------|------|
| 克制 | 色彩与装饰最小化。大面积使用中性色，品牌色只出现在关键操作和信息高亮处。 |
| 清晰 | 层级分明，信息密度适中。表格、表单、卡片边界清晰可辨。 |
| 高效 | 管理面板的核心是操作效率。减少视觉噪音，突出可操作元素。 |
| 一致 | 同类元素使用相同的颜色、间距、圆角和交互反馈。 |

---

## 2. 颜色系统

### 2.1 品牌色（Primary）

基于 Element Plus 默认蓝色体系，微调饱和度使其更沉稳。

| 语义 | 变量名 | 色值 | 用途 |
|------|--------|------|------|
| 品牌主色 | `--color-primary` | `#409EFF` | 主按钮、链接、选中态、进度条 |
| 浅色背景 | `--color-primary-light-3` | `#79BBFF` | 悬停态背景 |
| 更浅背景 | `--color-primary-light-5` | `#A0CFFF` | 禁用态背景 |
| 极浅背景 | `--color-primary-light-7` | `#C6E2FF` | 选中行背景、标签背景 |
| 淡色背景 | `--color-primary-light-8` | `#D9ECFF` | 提示条背景 |
| 极淡背景 | `--color-primary-light-9` | `#ECF5FF` | 悬浮卡片高亮边框 |
| 深色 | `--color-primary-dark-2` | `#337ECC` | 按钮按下态 |

### 2.2 功能色（Functional）

| 语义 | 变量名 | 色值 | 用途 |
|------|--------|------|------|
| 成功 | `--color-success` | `#67C23A` | 操作成功、在线状态、启用态 |
| 成功浅 | `--color-success-light-3` | `#95D475` | — |
| 成功极浅 | `--color-success-light-9` | `#E1F3D8` | 成功提示背景 |
| 警告 | `--color-warning` | `#E6A23C` | 待处理、即将过期 |
| 警告浅 | `--color-warning-light-3` | `#EEBE77` | — |
| 警告极浅 | `--color-warning-light-9` | `#FAECD8` | 警告提示背景 |
| 危险 | `--color-danger` | `#F56C6C` | 删除确认、错误、离线状态 |
| 危险浅 | `--color-danger-light-3` | `#F89898` | — |
| 危险极浅 | `--color-danger-light-9` | `#FDE2E2` | 危险提示背景、错误输入框边框 |
| 信息 | `--color-info` | `#909399` | 次要说明、禁用图标 |
| 信息极浅 | `--color-info-light-9` | `#E9E9EB` | 灰色提示背景 |

### 2.3 中性色（Neutral）

用于文字、背景、边框、分割线，构成界面的主体。

| 语义 | 变量名 | 色值 | 用途 |
|------|--------|------|------|
| 主文字 | `--color-text-primary` | `#303133` | 标题、正文、表格数据 |
| 次文字 | `--color-text-regular` | `#606266` | 次要正文、表单标签 |
| 占位文字 | `--color-text-secondary` | `#909399` | 提示文字、占位符、副标题 |
| 禁用文字 | `--color-text-placeholder` | `#C0C4CC` | 禁用态文字、空态文字 |
| 主边框 | `--color-border` | `#DCDFE6` | 输入框、卡片、表格边框 |
| 浅边框 | `--color-border-light` | `#E4E7ED` | 分割线、内部分隔 |
| 极浅边框 | `--color-border-lighter` | `#EBEEF5` | 卡片内部分隔 |
| 最浅边框 | `--color-border-extra-light` | `#F2F6FC` | 悬浮态额外边框 |
| 基础白 | `--color-bg-white` | `#FFFFFF` | 卡片、表单、弹窗背景 |
| 页面背景 | `--color-bg-page` | `#F5F7FA` | 页面整体背景 |
| 填充背景 | `--color-bg-fill` | `#F0F2F5` | 表头、禁用输入框背景 |
| 浅填充 | `--color-bg-fill-light` | `#F5F7FA` | 条纹行背景 |
| 极浅填充 | `--color-bg-fill-lighter` | `#FAFAFA` | 悬浮行背景 |
| 遮罩 | `--color-mask` | `rgba(0,0,0,0.5)` | 弹窗遮罩层 |

---

## 3. 布局规范

### 3.1 整体布局

采用经典管理面板布局，参考 1Panel / 宝塔面板 / Ant Design Pro：

```
┌──────────────────────────────────────────────┐
│  顶部导航栏 (Header)            高度: 56px   │
├────────┬─────────────────────────────────────┤
│        │                                     │
│  侧边栏 │          主内容区                   │
│ (Aside) │        (Main)                      │
│ 宽度:   │        padding: 24px               │
│  200px  │                                    │
│         │                                     │
│         │                                     │
└────────┴─────────────────────────────────────┘
```

- **顶部导航栏**：品牌标识 + 面包屑（可选）+ 用户操作区（退出登录）。固定高度 56px，背景白色，底边框 1px `--color-border-light`。
- **侧边栏**：导航菜单，宽度 200px，背景白色，右边框 1px `--color-border-light`。当前页高亮使用品牌色。
- **主内容区**：背景 `--color-bg-page`，内容居中最大宽度 1200px，内边距 24px。

### 3.2 登录页布局

独立全屏居中卡片，不受侧边栏约束（当前已实现）。

- 卡片宽度 360px，白色背景，8px 圆角，1px `--color-border` 边框
- 垂直水平居中，背景 `--color-bg-page`

### 3.3 内容卡片

页面中的功能区块使用白色卡片包裹：

- 背景 `--color-bg-white`
- 圆角 8px
- 边框 1px `--color-border`
- 内边距 20px
- 卡片间距 16px
- 卡片标题 16px 字号，下边距 16px

### 3.4 响应式断点

管理面板以桌面端为主，暂不投入移动端适配。

| 断点 | 宽度 | 行为 |
|------|------|------|
| xl | ≥1200px | 完整布局 |
| lg | ≥992px | 侧边栏可折叠 |
| md | ≥768px | 侧边栏收起为图标 |
| sm | <768px | 暂不处理 |

---

## 4. 字体规范

### 4.1 字体栈

```css
font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto,
  'Helvetica Neue', Arial, 'Noto Sans SC', sans-serif;
```

优先使用系统字体栈，中文环境加载 `Noto Sans SC`。不引入外部字体文件。

### 4.2 字号层级

| 层级 | 字号 | 字重 | 行高 | 用途 |
|------|------|------|------|------|
| H1 | 20px | 600 | 28px | 页面标题 |
| H2 | 16px | 600 | 24px | 卡片标题、区块标题 |
| H3 | 14px | 600 | 22px | 小节标题 |
| 正文 | 14px | 400 | 22px | 表格数据、表单标签、说明文字 |
| 辅助 | 13px | 400 | 20px | 副标题、表头、时间戳 |
| 小字 | 12px | 400 | 20px | 标签、角标、提示文字 |

---

## 5. 间距与圆角

### 5.1 间距

采用 4px 基准网格，常用间距值：

| 语义 | 值 | 用途 |
|------|----|------|
| xs | 4px | 图标与文字间距、紧凑行内间距 |
| sm | 8px | 表单项间距、标签间距 |
| md | 12px | 列表项间距、按钮组间距 |
| base | 16px | 卡片内间距、卡片间距 |
| lg | 20px | 卡片内边距 |
| xl | 24px | 页面内边距、区块间距 |
| 2xl | 32px | 大区块分隔 |

### 5.2 圆角

| 语义 | 值 | 用途 |
|------|----|------|
| small | 4px | 按钮、输入框、标签 |
| base | 8px | 卡片、弹窗、下拉面板 |
| large | 12px | 大卡片、hero 区域 |

---

## 6. 组件视觉规范

### 6.1 按钮

- **主按钮**：品牌色实心，白字。悬停用 `primary-light-3`，按下用 `primary-dark-2`。
- **次按钮**：白底，`--color-border` 边框，`--color-text-regular` 文字。
- **危险按钮**：`--color-danger` 实心，用于删除等不可逆操作。
- **文字按钮**：无边框无背景，品牌色文字。
- 按钮高度统一 32px（default），小尺寸 24px（small）。

### 6.2 表格

- 表头背景 `--color-bg-fill`，字重 600，字号 13px，文字色 `--color-text-regular`。
- 行高 48px，斑马纹用 `--color-bg-fill-lighter`。
- 悬浮行背景 `--color-bg-fill-lighter`。
- 选中行背景 `--color-primary-light-7`。
- 边框使用 `--color-border-lighter`，不使用粗边框。
- 行可点击时显示 `cursor: pointer`。

### 6.3 表单

- 标签字号 14px，颜色 `--color-text-regular`，右对齐（label-width 80px 或 100px）。
- 输入框高度 32px，边框 `--color-border`，聚焦边框 `--color-primary`。
- 校验错误：边框 `--color-danger`，错误文字 12px `--color-danger`。
- 表单项间距 18px。

### 6.4 弹窗（Dialog）

- 圆角 8px，标题字号 16px 字重 600。
- 底部按钮区右对齐，主按钮在右。
- 遮罩 `--color-mask`。

### 6.5 状态标签（Tag）

- 圆角 4px，字号 12px，高度 22px。
- 运行中：success 浅色标签（绿底绿字）。
- 已停止：info 浅色标签（灰底灰字）。
- 错误：danger 浅色标签（红底红字）。

### 6.6 空态

- 居中文案 `--color-text-secondary`，字号 14px。
- 可选上方一个 64px 灰色图标。
- 上下内边距 40px。

---

## 7. 交互反馈规范

### 7.1 加载态

- 页面级：居中 spinner + "加载中..." 文字。
- 按钮级：按钮内显示旋转 icon，禁用点击。
- 表格级：使用 Element Plus 的 `v-loading` 指令。

### 7.2 操作反馈

- 成功：`ElMessage.success`，绿色提示，2 秒自动消失。
- 失败：`ElMessage.error`，红色提示，3 秒自动消失。
- 警告：`ElMessage.warning`，橙色提示，3 秒自动消失。
- 危险操作（删除）：使用 `ElMessageBox.confirm` 二次确认，确认按钮为 danger 类型。

### 7.3 路由跳转

- 登录成功后跳转管理页。
- 未初始化跳转 `/init`，未认证跳转 `/login`。
- 使用 `router.replace` 避免回退到认证页。

---

## 8. 暗色模式（预留）

当前仅实现亮色模式。暗色模式作为后续扩展，在此预留变量映射关系：

| 亮色变量 | 暗色映射 |
|----------|----------|
| `--color-text-primary: #303133` | `#CFD3DC` |
| `--color-text-regular: #606266` | `#A4A7AE` |
| `--color-text-secondary: #909399` | `#6C6E72` |
| `--color-bg-white: #FFFFFF` | `#1D1E1F` |
| `--color-bg-page: #F5F7FA` | `#141414` |
| `--color-border: #DCDFE6` | `#4C4D4F` |
| `--color-border-light: #E4E7ED` | `#414243` |

实现时通过 `html.dark` 类名切换 CSS 变量即可，无需修改组件代码。

---

## 9. 实现约定

### 9.1 变量命名

所有自定义颜色变量统一放在 `src/styles/main.css` 的 `:root` 中，使用 `--color-*` 命名。Element Plus 组件使用自身 `--el-*` 变量，不重复定义。

当前 `main.css` 中的旧变量映射：

| 旧变量 | 新变量（本规范） |
|--------|-----------------|
| `--primary-color` | `--color-primary` |
| `--text-color` | `--color-text-primary` |
| `--text-color-secondary` | `--color-text-secondary` |
| `--border-color` | `--color-border` |
| `--background-color` | `--color-bg-page` |

迁移时逐步替换，不要求一次性全部改完。

### 9.2 样式作用域

组件样式一律使用 `<style scoped>`，避免全局污染。需要穿透 Element Plus 组件时使用 `:deep()`。

### 9.3 禁止事项

- 不使用 `!important`（Element Plus 主题定制除外）。
- 不在 CSS 中硬编码 Element Plus 已有的颜色值，使用对应的 CSS 变量。
- 不引入额外的 UI 框架或图标库（图标使用 Element Plus 内置图标）。
- 不使用渐变色（管理面板保持平实）。
- 不使用大面积深色块（信息密度优先于视觉冲击）。
