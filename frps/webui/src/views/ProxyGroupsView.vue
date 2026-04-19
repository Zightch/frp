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
const proxyGroups = ref<ProxyGroup[]>([]);
const formRef = ref<FormInstance>();
const editingId = ref<number | null>(null);
const revealedToken = reactive({
  groupName: "",
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
    pageError.value = "";
  } catch (error) {
    pageError.value = resolveMessage(error, "分组列表读取失败");
  } finally {
    loading.value = false;
  }
}

function showTokenDialog(groupName: string, token: string): void {
  revealedToken.groupName = groupName;
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
    showTokenDialog(created.item.name, created.token);
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
    showTokenDialog(result.item.name, result.token);
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
  <section class="page-stack">
    <div class="management-hero-grid">
      <el-card class="hero-card hero-card--feature">
        <p class="page-eyebrow">Phase 4.1</p>
        <h2 class="page-title">分组管理</h2>
        <p class="page-copy">
          当前页面已经直接接入 <code>/api/v1/proxy-groups</code>。可以在这里创建、编辑、删除分组，并重置对应
          frpc 接入 token。
        </p>

        <div class="hero-actions">
          <el-button
            type="primary"
            @click="openCreateDialog"
          >
            新建分组
          </el-button>
          <el-button
            plain
            :loading="loading"
            @click="loadProxyGroups"
          >
            刷新列表
          </el-button>
        </div>
      </el-card>

      <div class="metric-grid">
        <el-card class="metric-card">
          <span class="metric-label">分组总数</span>
          <strong class="metric-value">{{ totalCount }}</strong>
        </el-card>

        <el-card class="metric-card">
          <span class="metric-label">启用分组</span>
          <strong class="metric-value">{{ enabledCount }}</strong>
        </el-card>

        <el-card class="metric-card">
          <span class="metric-label">停用分组</span>
          <strong class="metric-value">{{ disabledCount }}</strong>
        </el-card>
      </div>
    </div>

    <el-alert
      v-if="pageError"
      type="warning"
      :closable="false"
      show-icon
      :title="pageError"
    />

    <el-card class="glass-panel">
      <template #header>
        <div class="management-card-header">
          <div>
            <div class="panel-header">分组列表</div>
            <p class="management-card-copy">
              token 只会在创建和重置时返回一次，当前页不回显历史 token 明文。
            </p>
          </div>

          <el-button
            type="primary"
            plain
            @click="openCreateDialog"
          >
            新建分组
          </el-button>
        </div>
      </template>

      <el-empty
        v-if="!loading && proxyGroups.length === 0"
        description="当前还没有分组，先创建第一个接入分组。"
      />

      <el-table
        v-else
        v-loading="loading"
        :data="proxyGroups"
        stripe
      >
        <el-table-column
          prop="id"
          label="ID"
          width="80"
        />
        <el-table-column
          prop="name"
          label="分组名"
          min-width="180"
        />
        <el-table-column
          prop="tokenId"
          label="Token ID"
          min-width="220"
        />
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
          label="客户端访问"
          min-width="120"
        >
          <template #default="{ row }">
            {{ formatAccessMode(row.clientAccessMode) }}
          </template>
        </el-table-column>
        <el-table-column
          label="隧道入口访问"
          min-width="140"
        >
          <template #default="{ row }">
            {{ formatAccessMode(row.tunnelAccessMode) }}
          </template>
        </el-table-column>
        <el-table-column
          label="更新时间"
          min-width="180"
        >
          <template #default="{ row }">
            {{ formatDate(row.updatedAt) }}
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
    </el-card>

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
      title="分组 Token"
      width="620px"
    >
      <p class="management-card-copy">
        分组 <strong>{{ revealedToken.groupName }}</strong> 的 token 只展示这一次，请立即保存给对应 frpc 使用。
      </p>

      <div class="secret-panel">
        <code class="secret-code">{{ revealedToken.token }}</code>
      </div>

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
