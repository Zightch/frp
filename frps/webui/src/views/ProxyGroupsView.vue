<script setup lang="ts">
import { computed, onMounted, reactive, ref } from "vue";
import { ElMessage, ElMessageBox, type FormInstance, type FormRules } from "element-plus";

import {
  createProxyGroup,
  deleteProxyGroup,
  fetchProxyGroups,
  resetProxyGroupToken,
  updateProxyGroup,
  type ProxyGroup,
} from "@/api/management";
import { ApiError } from "@/api/http";

const knownErrorMessages: Record<string, string> = {
  "management store is unavailable": "管理存储当前不可用。",
  "name is required": "请输入分组名称。",
  "proxy group name already exists": "分组名称已存在。",
  "proxy group not found": "分组不存在，列表将自动刷新。",
};

const loading = ref(false);
const submitting = ref(false);
const dialogVisible = ref(false);
const tokenDialogVisible = ref(false);
const pageError = ref("");
const refreshedAt = ref("");
const proxyGroups = ref<ProxyGroup[]>([]);
const formRef = ref<FormInstance>();
const editingId = ref<number | null>(null);
const revealedToken = reactive({
  groupName: "",
  tokenId: "",
  token: "",
});
const form = reactive({
  name: "",
  enabled: true,
});

const isEditing = computed(() => editingId.value !== null);
const totalCount = computed(() => proxyGroups.value.length);
const enabledCount = computed(() => proxyGroups.value.filter((item) => item.enabled).length);
const disabledCount = computed(() => totalCount.value - enabledCount.value);
const latestUpdatedGroup = computed(() => {
  let latest: ProxyGroup | null = null;
  let latestTimestamp = -1;

  for (const item of proxyGroups.value) {
    const timestamp = resolveTimestamp(item.updatedAt) ?? resolveTimestamp(item.createdAt) ?? -1;
    if (timestamp > latestTimestamp) {
      latest = item;
      latestTimestamp = timestamp;
    }
  }

  return latest;
});
const latestUpdatedGroupName = computed(() => latestUpdatedGroup.value?.name ?? "暂无分组");
const latestUpdatedAtLabel = computed(() => {
  if (!latestUpdatedGroup.value) {
    return "暂无更新";
  }

  return formatDate(latestUpdatedGroup.value.updatedAt || latestUpdatedGroup.value.createdAt);
});
const refreshedAtLabel = computed(() => {
  if (loading.value && !refreshedAt.value) {
    return "首次读取中";
  }

  if (!refreshedAt.value) {
    return "尚未同步";
  }

  return formatDate(refreshedAt.value);
});

const formRules: FormRules = {
  name: [
    {
      required: true,
      message: "请输入分组名称",
      trigger: "blur",
    },
    {
      min: 1,
      max: 128,
      message: "分组名称长度需在 1 到 128 个字符之间",
      trigger: "blur",
    },
  ],
};

function resolveMessage(error: unknown, fallback: string): string {
  if (error instanceof ApiError) {
    return knownErrorMessages[error.message] ?? error.message;
  }

  if (typeof error === "object" && error !== null && "message" in error) {
    const message = (error as { message?: unknown }).message;
    if (typeof message === "string" && message.trim() !== "") {
      return knownErrorMessages[message] ?? message;
    }
  }

  return fallback;
}

function resolveTimestamp(value: string): number | null {
  if (!value) {
    return null;
  }

  const parsed = new Date(value);
  if (Number.isNaN(parsed.getTime())) {
    return null;
  }

  return parsed.getTime();
}

function formatDate(value: string): string {
  if (!value) {
    return "-";
  }

  const parsed = new Date(value);
  if (Number.isNaN(parsed.getTime())) {
    return value;
  }

  return parsed.toLocaleString("zh-CN", { hour12: false });
}

function formatAccessMode(value: string): string {
  if (!value || value === "disabled") {
    return "未启用";
  }
  return value;
}

function resetForm(): void {
  form.name = "";
  form.enabled = true;
  formRef.value?.clearValidate();
}

function openCreateDialog(): void {
  editingId.value = null;
  resetForm();
  dialogVisible.value = true;
}

function openEditDialog(item: ProxyGroup): void {
  editingId.value = item.id;
  form.name = item.name;
  form.enabled = item.enabled;
  formRef.value?.clearValidate();
  dialogVisible.value = true;
}

async function loadProxyGroups(): Promise<void> {
  loading.value = true;

  try {
    proxyGroups.value = await fetchProxyGroups();
    refreshedAt.value = new Date().toISOString();
    pageError.value = "";
  } catch (error) {
    pageError.value = resolveMessage(error, "分组列表读取失败");
  } finally {
    loading.value = false;
  }
}

function showTokenDialog(item: ProxyGroup, token: string): void {
  revealedToken.groupName = item.name;
  revealedToken.tokenId = item.tokenId;
  revealedToken.token = token;
  tokenDialogVisible.value = true;
}

async function handleSubmit(): Promise<void> {
  if (!formRef.value) {
    return;
  }

  const valid = await formRef.value.validate().catch(() => false);
  if (!valid) {
    return;
  }

  submitting.value = true;

  try {
    const payload = {
      name: form.name.trim(),
      enabled: form.enabled,
    };

    if (isEditing.value && editingId.value !== null) {
      const item = await updateProxyGroup(editingId.value, payload);
      await loadProxyGroups();
      dialogVisible.value = false;
      ElMessage.success(`分组“${item.name}”已更新。`);
      return;
    }

    const created = await createProxyGroup(payload);
    await loadProxyGroups();
    dialogVisible.value = false;
    showTokenDialog(created.item, created.token);
    ElMessage.success(`分组“${created.item.name}”已创建。`);
  } catch (error) {
    ElMessage.error(resolveMessage(error, "分组保存失败"));
  } finally {
    submitting.value = false;
  }
}

async function handleDelete(item: ProxyGroup): Promise<void> {
  try {
    await ElMessageBox.confirm(
      `删除分组“${item.name}”后，其下所有隧道也会一并删除。该操作不可恢复。`,
      "删除分组",
      {
        type: "warning",
        confirmButtonText: "删除",
        cancelButtonText: "取消",
      },
    );
  } catch {
    return;
  }

  try {
    await deleteProxyGroup(item.id);
    await loadProxyGroups();
    ElMessage.success(`分组“${item.name}”已删除。`);
  } catch (error) {
    ElMessage.error(resolveMessage(error, "分组删除失败"));
  }
}

async function handleResetToken(item: ProxyGroup): Promise<void> {
  try {
    await ElMessageBox.confirm(
      `重置分组“${item.name}”的接入 token 后，旧 token 将立即失效。`,
      "重置 Token",
      {
        type: "warning",
        confirmButtonText: "重置",
        cancelButtonText: "取消",
      },
    );
  } catch {
    return;
  }

  try {
    const result = await resetProxyGroupToken(item.id);
    await loadProxyGroups();
    showTokenDialog(result.item, result.token);
    ElMessage.success(`分组“${result.item.name}”的 token 已重置。`);
  } catch (error) {
    ElMessage.error(resolveMessage(error, "分组 token 重置失败"));
  }
}

async function copyToken(): Promise<void> {
  if (!revealedToken.token) {
    return;
  }

  if (!navigator.clipboard?.writeText) {
    ElMessage.warning("当前环境不支持剪贴板复制，请手动复制 token。");
    return;
  }

  try {
    await navigator.clipboard.writeText(revealedToken.token);
    ElMessage.success("Token 已复制到剪贴板。");
  } catch {
    ElMessage.warning("复制失败，请手动复制 token。");
  }
}

onMounted(() => {
  void loadProxyGroups();
});
</script>

<template>
  <section class="page-stack proxy-groups-page">
    <div class="page-header">
      <div class="page-header__copy">
        <p class="page-section-label">group lifecycle</p>
        <h3 class="page-section-title">统一管理 frpc 接入分组、启停状态与一次性 token 生命周期</h3>
        <p class="page-copy">
          分组既是 frpc 持有 token 的归属，也是后续隧道配置的所属容器。当前页面只负责分组实体、启停状态和
          token 管理，不重复承接隧道配置本身。
        </p>
        <p class="page-inline-note">最近同步：{{ refreshedAtLabel }}。列表页只显示 Token ID，不回显历史 token 明文。</p>
      </div>

      <div class="page-actions">
        <el-button
          plain
          :loading="loading"
          @click="loadProxyGroups"
        >
          刷新列表
        </el-button>
        <el-button
          type="primary"
          @click="openCreateDialog"
        >
          新建分组
        </el-button>
      </div>
    </div>

    <div
      v-if="pageError"
      class="page-feedback"
    >
      <el-alert
        type="warning"
        :closable="false"
        show-icon
        :title="pageError"
      />
    </div>

    <div class="page-metrics">
      <el-card class="metric-card">
        <span class="metric-label">分组总数</span>
        <strong class="metric-value">{{ totalCount }}</strong>
        <p class="metric-note">所有 frpc 接入和隧道归属都从这里开始建立。</p>
      </el-card>

      <el-card class="metric-card">
        <span class="metric-label">启用分组</span>
        <strong class="metric-value">{{ enabledCount }}</strong>
        <p class="metric-note">当前列表里处于启用状态的分组数。</p>
      </el-card>

      <el-card class="metric-card">
        <span class="metric-label">停用分组</span>
        <strong class="metric-value">{{ disabledCount }}</strong>
        <p class="metric-note">当前列表里处于停用状态的分组数。</p>
      </el-card>

      <el-card class="metric-card">
        <span class="metric-label">最近更新分组</span>
        <strong class="metric-value metric-value--compact">{{ latestUpdatedGroupName }}</strong>
        <p class="metric-note">{{ latestUpdatedAtLabel }}</p>
      </el-card>
    </div>

    <div class="page-body">
      <div class="page-main">
        <el-card class="glass-panel page-section-card">
          <template #header>
            <div class="management-card-header">
              <div>
                <div class="panel-header">分组列表</div>
                <p class="management-card-copy">
                  Token ID 用于标识当前生效凭据版本；如果 frpc 侧丢失明文 token，只能通过重置领取新的 token。
                </p>
              </div>
            </div>
          </template>

          <el-empty
            v-if="!loading && proxyGroups.length === 0"
            description="当前还没有分组，先创建第一个接入分组。"
          />

          <div
            v-else
            class="management-table-wrap"
          >
            <el-table
              v-loading="loading"
              class="management-data-table"
              :data="proxyGroups"
              stripe
            >
              <el-table-column
                label="分组"
                min-width="220"
              >
                <template #default="{ row }">
                  <div class="entity-stack">
                    <strong class="entity-stack__title">{{ row.name }}</strong>
                    <span class="entity-stack__meta">ID #{{ row.id }}</span>
                  </div>
                </template>
              </el-table-column>
              <el-table-column
                label="Token ID"
                min-width="240"
              >
                <template #default="{ row }">
                  <code class="token-id-text">{{ row.tokenId }}</code>
                </template>
              </el-table-column>
              <el-table-column
                label="状态"
                width="100"
              >
                <template #default="{ row }">
                  <el-tag :type="row.enabled ? 'success' : 'info'">
                    {{ row.enabled ? "启用" : "停用" }}
                  </el-tag>
                </template>
              </el-table-column>
              <el-table-column
                label="接入约束"
                min-width="180"
              >
                <template #default="{ row }">
                  <div class="entity-stack entity-stack--tight">
                    <span class="entity-stack__meta">客户端：{{ formatAccessMode(row.clientAccessMode) }}</span>
                    <span class="entity-stack__meta">隧道入口：{{ formatAccessMode(row.tunnelAccessMode) }}</span>
                  </div>
                </template>
              </el-table-column>
              <el-table-column
                label="时间"
                min-width="210"
              >
                <template #default="{ row }">
                  <div class="entity-stack entity-stack--tight">
                    <span class="entity-stack__meta">创建：{{ formatDate(row.createdAt) }}</span>
                    <span class="entity-stack__meta">更新：{{ formatDate(row.updatedAt) }}</span>
                  </div>
                </template>
              </el-table-column>
              <el-table-column
                label="操作"
                min-width="240"
                fixed="right"
              >
                <template #default="{ row }">
                  <div class="management-table-actions">
                    <el-button
                      link
                      type="primary"
                      @click="openEditDialog(row)"
                    >
                      编辑
                    </el-button>
                    <el-button
                      link
                      type="warning"
                      @click="handleResetToken(row)"
                    >
                      重置 Token
                    </el-button>
                    <el-button
                      link
                      type="danger"
                      @click="handleDelete(row)"
                    >
                      删除
                    </el-button>
                  </div>
                </template>
              </el-table-column>
            </el-table>
          </div>
        </el-card>
      </div>

      <div class="page-side">
        <el-card class="glass-panel page-section-card">
          <template #header>
            <div class="management-card-header">
              <div>
                <div class="panel-header">Token 生命周期</div>
                <p class="management-card-copy">这一侧只强调当前真实存在的 token 使用约束。</p>
              </div>
            </div>
          </template>

          <ul class="panel-list">
            <li>创建分组时，服务端只返回一次明文 token；关闭弹窗后当前页面不会再回显。</li>
            <li>重置 token 会立即让旧 token 失效，frpc 需要同步替换为新 token。</li>
            <li>列表页长期保留的是 Token ID，用来识别当前凭据版本，不是可直接使用的明文 token。</li>
            <li>如果只是修改分组名称或启停状态，直接编辑分组即可，不会发放新的 token。</li>
          </ul>
        </el-card>

        <el-card class="glass-panel page-section-card">
          <template #header>
            <div class="management-card-header">
              <div>
                <div class="panel-header">当前画像</div>
                <p class="management-card-copy">右侧摘要只来自当前已读取到的分组列表，不引入未来能力描述。</p>
              </div>
            </div>
          </template>

          <div class="overview-list">
            <div class="overview-row">
              <div class="overview-row__content">
                <span class="overview-row__label">最近同步</span>
                <p class="overview-row__copy">当前管理会话已重新拉取分组列表与 token_id 快照。</p>
              </div>
              <strong class="overview-row__value">{{ refreshedAtLabel }}</strong>
            </div>

            <div class="overview-row">
              <div class="overview-row__content">
                <span class="overview-row__label">启停分布</span>
                <p class="overview-row__copy">
                  当前共有 {{ totalCount }} 个分组，其中启用 {{ enabledCount }} 个、停用 {{ disabledCount }} 个。
                </p>
              </div>
              <strong class="overview-row__value">{{ enabledCount }} / {{ totalCount }}</strong>
            </div>

            <div class="overview-row">
              <div class="overview-row__content">
                <span class="overview-row__label">最近更新</span>
                <p class="overview-row__copy">最近一次变更来自当前列表中的最新更新时间，不区分创建或编辑。</p>
              </div>
              <strong class="overview-row__value">{{ latestUpdatedGroupName }}</strong>
            </div>

            <div class="overview-row">
              <div class="overview-row__content">
                <span class="overview-row__label">下游依赖</span>
                <p class="overview-row__copy">
                  {{
                    totalCount === 0
                      ? "当前还没有可供隧道选择的分组。"
                      : "隧道创建时只能归属于这里已经存在的分组。"
                  }}
                </p>
              </div>
              <strong class="overview-row__value">{{ totalCount === 0 ? "需先建组" : "可用于隧道" }}</strong>
            </div>
          </div>
        </el-card>
      </div>
    </div>

    <el-dialog
      v-model="dialogVisible"
      :title="isEditing ? '编辑分组' : '新建分组'"
      width="520px"
      destroy-on-close
    >
      <el-form
        ref="formRef"
        :model="form"
        :rules="formRules"
        label-position="top"
        status-icon
      >
        <el-form-item
          label="分组名称"
          prop="name"
        >
          <el-input
            v-model="form.name"
            maxlength="128"
            show-word-limit
            placeholder="例如：default-group"
          />
        </el-form-item>

        <el-form-item label="启用状态">
          <el-switch
            v-model="form.enabled"
            inline-prompt
            active-text="启用"
            inactive-text="停用"
          />
        </el-form-item>

        <p class="management-form-note">
          {{
            isEditing
              ? "编辑分组不会返回新的 token；如需替换 frpc 凭据，请在列表中执行“重置 Token”。"
              : "创建分组后会返回一次性明文 token，请在弹窗中立即保存给对应 frpc 使用。"
          }}
        </p>
      </el-form>

      <template #footer>
        <div class="management-dialog-actions">
          <el-button @click="dialogVisible = false">
            取消
          </el-button>
          <el-button
            type="primary"
            :loading="submitting"
            @click="handleSubmit"
          >
            {{ isEditing ? "保存变更" : "创建分组" }}
          </el-button>
        </div>
      </template>
    </el-dialog>

    <el-dialog
      v-model="tokenDialogVisible"
      title="一次性分组 Token"
      width="640px"
      destroy-on-close
    >
      <p class="management-card-copy">
        分组 <strong>{{ revealedToken.groupName }}</strong> 的 token 只展示这一次，请立即保存给对应 frpc 使用。
      </p>

      <div class="token-reveal-grid">
        <div class="token-reveal-card">
          <span class="page-section-label">group</span>
          <strong>{{ revealedToken.groupName }}</strong>
        </div>

        <div class="token-reveal-card">
          <span class="page-section-label">token id</span>
          <code>{{ revealedToken.tokenId }}</code>
        </div>
      </div>

      <div class="secret-panel">
        <code class="secret-code">{{ revealedToken.token }}</code>
      </div>

      <p class="management-form-note">
        关闭此弹窗后，管理页只会保留上面的 Token ID，不会再次显示明文 token。
      </p>

      <template #footer>
        <div class="management-dialog-actions">
          <el-button
            plain
            @click="copyToken"
          >
            复制 Token
          </el-button>
          <el-button
            type="primary"
            @click="tokenDialogVisible = false"
          >
            我已保存
          </el-button>
        </div>
      </template>
    </el-dialog>
  </section>
</template>
