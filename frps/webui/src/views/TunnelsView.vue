<script setup lang="ts">
import { computed, onMounted, reactive, ref, watch } from "vue";
import { ElMessage, ElMessageBox, type FormInstance, type FormRules } from "element-plus";

import {
  createTunnel,
  deleteTunnel,
  fetchProxyGroups,
  fetchTunnels,
  updateTunnel,
  type ProxyGroup,
  type Tunnel,
  type TunnelProtocol,
  type TunnelRemoteType,
} from "@/api/management";
import { ApiError } from "@/api/http";

const knownErrorMessages: Record<string, string> = {
  "group_id does not exist": "所选分组不存在，请刷新后重试。",
  "group_id must be greater than zero": "请选择分组。",
  "local_host is invalid": "本地地址格式无效。",
  "management store is unavailable": "管理存储当前不可用。",
  "name is required": "请输入隧道名称。",
  "port range end must be greater than or equal to start": "结束端口不能小于起始端口。",
  "ports must be between 1 and 65535": "端口范围必须在 1 到 65535 之间。",
  "protocol must be tcp or udp": "协议只支持 TCP 或 UDP。",
  "remote and local port ranges must be aligned": "远端和本地端口范围跨度必须一致。",
  "remote_type must be single or range": "远端映射类型无效。",
  "single tunnel must use identical start and end ports": "单端口映射时，起始端口和结束端口必须一致。",
  "tunnel name already exists in the selected proxy group": "该分组下已经存在同名隧道。",
  "tunnel not found": "隧道不存在，列表将自动刷新。",
};

type TunnelForm = {
  groupId: number;
  name: string;
  protocol: TunnelProtocol;
  remoteType: TunnelRemoteType;
  remoteStart: number;
  remoteEnd: number;
  localHost: string;
  localStart: number;
  localEnd: number;
  enabled: boolean;
};

const loading = ref(false);
const submitting = ref(false);
const dialogVisible = ref(false);
const pageError = ref("");
const proxyGroups = ref<ProxyGroup[]>([]);
const tunnels = ref<Tunnel[]>([]);
const formRef = ref<FormInstance>();
const editingId = ref<number | null>(null);
const form = reactive<TunnelForm>({
  groupId: 0,
  name: "",
  protocol: "tcp",
  remoteType: "single",
  remoteStart: 7000,
  remoteEnd: 7000,
  localHost: "127.0.0.1",
  localStart: 7000,
  localEnd: 7000,
  enabled: true,
});

const isEditing = computed(() => editingId.value !== null);
const hasProxyGroups = computed(() => proxyGroups.value.length > 0);
const totalCount = computed(() => tunnels.value.length);
const enabledCount = computed(() => tunnels.value.filter((item) => item.enabled).length);
const rangeCount = computed(() => tunnels.value.filter((item) => item.remoteType === "range").length);
const isSingleRemoteType = computed(() => form.remoteType === "single");

function isValidPort(value: number): boolean {
  return Number.isInteger(value) && value >= 1 && value <= 65535;
}

const formRules: FormRules = {
  groupId: [
    {
      validator: (_rule, value: number, callback) => {
        if (!Number.isInteger(value) || value <= 0) {
          callback(new Error("请选择分组"));
          return;
        }
        callback();
      },
      trigger: "change",
    },
  ],
  name: [
    {
      required: true,
      message: "请输入隧道名称",
      trigger: "blur",
    },
    {
      min: 1,
      max: 128,
      message: "隧道名称长度需在 1 到 128 个字符之间",
      trigger: "blur",
    },
  ],
  localHost: [
    {
      required: true,
      message: "请输入本地地址",
      trigger: "blur",
    },
  ],
  remoteStart: [
    {
      validator: (_rule, value: number, callback) => {
        if (!isValidPort(value)) {
          callback(new Error("远端起始端口需在 1 到 65535 之间"));
          return;
        }
        callback();
      },
      trigger: "change",
    },
  ],
  remoteEnd: [
    {
      validator: (_rule, value: number, callback) => {
        if (!isValidPort(value)) {
          callback(new Error("远端结束端口需在 1 到 65535 之间"));
          return;
        }
        if (value < form.remoteStart) {
          callback(new Error("远端结束端口不能小于起始端口"));
          return;
        }
        if (form.remoteType === "single" && value !== form.remoteStart) {
          callback(new Error("单端口映射时远端端口必须一致"));
          return;
        }
        callback();
      },
      trigger: "change",
    },
  ],
  localStart: [
    {
      validator: (_rule, value: number, callback) => {
        if (!isValidPort(value)) {
          callback(new Error("本地起始端口需在 1 到 65535 之间"));
          return;
        }
        callback();
      },
      trigger: "change",
    },
  ],
  localEnd: [
    {
      validator: (_rule, value: number, callback) => {
        if (!isValidPort(value)) {
          callback(new Error("本地结束端口需在 1 到 65535 之间"));
          return;
        }
        if (value < form.localStart) {
          callback(new Error("本地结束端口不能小于起始端口"));
          return;
        }
        if (form.remoteType === "single" && value !== form.localStart) {
          callback(new Error("单端口映射时本地端口必须一致"));
          return;
        }
        if (form.remoteType === "range") {
          const remoteSpan = form.remoteEnd - form.remoteStart;
          const localSpan = value - form.localStart;
          if (remoteSpan !== localSpan) {
            callback(new Error("远端和本地端口范围跨度必须一致"));
            return;
          }
        }
        callback();
      },
      trigger: "change",
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

function formatEndpoint(item: Tunnel, side: "remote" | "local"): string {
  if (side === "remote") {
    return item.remoteType === "single" ? `${item.remoteStart}` : `${item.remoteStart}-${item.remoteEnd}`;
  }

  return item.remoteType === "single"
    ? `${item.localHost}:${item.localStart}`
    : `${item.localHost}:${item.localStart}-${item.localEnd}`;
}

function resetForm(): void {
  const firstGroup = proxyGroups.value[0];
  form.groupId = firstGroup?.id ?? 0;
  form.name = "";
  form.protocol = "tcp";
  form.remoteType = "single";
  form.remoteStart = 7000;
  form.remoteEnd = 7000;
  form.localHost = "127.0.0.1";
  form.localStart = 7000;
  form.localEnd = 7000;
  form.enabled = true;
  formRef.value?.clearValidate();
}

function openCreateDialog(): void {
  if (!hasProxyGroups.value) {
    ElMessage.warning("请先创建分组，再创建隧道。");
    return;
  }

  editingId.value = null;
  resetForm();
  dialogVisible.value = true;
}

function openEditDialog(item: Tunnel): void {
  editingId.value = item.id;
  form.groupId = item.groupId;
  form.name = item.name;
  form.protocol = item.protocol;
  form.remoteType = item.remoteType;
  form.remoteStart = item.remoteStart;
  form.remoteEnd = item.remoteEnd;
  form.localHost = item.localHost;
  form.localStart = item.localStart;
  form.localEnd = item.localEnd;
  form.enabled = item.enabled;
  formRef.value?.clearValidate();
  dialogVisible.value = true;
}

async function loadPage(): Promise<void> {
  loading.value = true;

  try {
    const [groups, tunnelItems] = await Promise.all([fetchProxyGroups(), fetchTunnels()]);
    proxyGroups.value = groups;
    tunnels.value = tunnelItems;
    pageError.value = "";
  } catch (error) {
    pageError.value = resolveMessage(error, "隧道列表读取失败");
  } finally {
    loading.value = false;
  }
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
      groupId: form.groupId,
      name: form.name.trim(),
      protocol: form.protocol,
      remoteType: form.remoteType,
      remoteStart: form.remoteStart,
      remoteEnd: form.remoteEnd,
      localHost: form.localHost.trim(),
      localStart: form.localStart,
      localEnd: form.localEnd,
      enabled: form.enabled,
    };

    if (isEditing.value && editingId.value !== null) {
      const item = await updateTunnel(editingId.value, payload);
      await loadPage();
      dialogVisible.value = false;
      ElMessage.success(`隧道“${item.name}”已更新。`);
      return;
    }

    const item = await createTunnel(payload);
    await loadPage();
    dialogVisible.value = false;
    ElMessage.success(`隧道“${item.name}”已创建。`);
  } catch (error) {
    ElMessage.error(resolveMessage(error, "隧道保存失败"));
  } finally {
    submitting.value = false;
  }
}

async function handleDelete(item: Tunnel): Promise<void> {
  try {
    await ElMessageBox.confirm(
      `删除隧道“${item.name}”后，该映射将立即失效。该操作不可恢复。`,
      "删除隧道",
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
    await deleteTunnel(item.id);
    await loadPage();
    ElMessage.success(`隧道“${item.name}”已删除。`);
  } catch (error) {
    ElMessage.error(resolveMessage(error, "隧道删除失败"));
  }
}

watch(
  () => form.remoteType,
  (value) => {
    if (value === "single") {
      form.remoteEnd = form.remoteStart;
      form.localEnd = form.localStart;
    }
  },
);

watch(
  () => form.remoteStart,
  (value) => {
    if (form.remoteType === "single") {
      form.remoteEnd = value;
    }
  },
);

watch(
  () => form.localStart,
  (value) => {
    if (form.remoteType === "single") {
      form.localEnd = value;
    }
  },
);

onMounted(() => {
  void loadPage();
});
</script>

<template>
  <section class="page-stack">
    <div class="management-hero-grid">
      <el-card class="hero-card hero-card--feature">
        <p class="page-eyebrow">Phase 4.2</p>
        <h2 class="page-title">隧道管理</h2>
        <p class="page-copy">
          当前页面已经直接接入 <code>/api/v1/tunnels</code>。可以在这里管理 TCP/UDP 单端口映射与端口范围映射。
        </p>

        <div class="hero-actions">
          <el-button
            type="primary"
            :disabled="!hasProxyGroups"
            @click="openCreateDialog"
          >
            新建隧道
          </el-button>
          <el-button
            plain
            :loading="loading"
            @click="loadPage"
          >
            刷新列表
          </el-button>
        </div>
      </el-card>

      <div class="metric-grid">
        <el-card class="metric-card">
          <span class="metric-label">隧道总数</span>
          <strong class="metric-value">{{ totalCount }}</strong>
        </el-card>

        <el-card class="metric-card">
          <span class="metric-label">启用隧道</span>
          <strong class="metric-value">{{ enabledCount }}</strong>
        </el-card>

        <el-card class="metric-card">
          <span class="metric-label">范围映射</span>
          <strong class="metric-value">{{ rangeCount }}</strong>
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

    <el-alert
      v-if="!hasProxyGroups"
      type="info"
      :closable="false"
      show-icon
      title="当前还没有分组，请先到分组管理页创建分组。"
    />

    <el-card class="glass-panel">
      <template #header>
        <div class="management-card-header">
          <div>
            <div class="panel-header">隧道列表</div>
            <p class="management-card-copy">
              隧道名称在同一分组内必须唯一；范围映射要求远端和本地端口跨度保持一致。
            </p>
          </div>

          <el-button
            type="primary"
            plain
            :disabled="!hasProxyGroups"
            @click="openCreateDialog"
          >
            新建隧道
          </el-button>
        </div>
      </template>

      <el-empty
        v-if="!loading && tunnels.length === 0"
        description="当前还没有隧道映射。"
      />

      <el-table
        v-else
        v-loading="loading"
        :data="tunnels"
        stripe
      >
        <el-table-column
          prop="id"
          label="ID"
          width="80"
        />
        <el-table-column
          prop="groupName"
          label="分组"
          min-width="140"
        />
        <el-table-column
          prop="name"
          label="隧道名"
          min-width="180"
        />
        <el-table-column
          label="协议"
          width="100"
        >
          <template #default="{ row }">
            <el-tag :type="row.protocol === 'tcp' ? 'primary' : 'success'">
              {{ row.protocol.toUpperCase() }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column
          label="远端映射"
          min-width="140"
        >
          <template #default="{ row }">
            {{ formatEndpoint(row, "remote") }}
          </template>
        </el-table-column>
        <el-table-column
          label="本地目标"
          min-width="220"
        >
          <template #default="{ row }">
            {{ formatEndpoint(row, "local") }}
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
          label="更新时间"
          min-width="180"
        >
          <template #default="{ row }">
            {{ formatDate(row.updatedAt) }}
          </template>
        </el-table-column>
        <el-table-column
          label="操作"
          min-width="170"
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
      :title="isEditing ? '编辑隧道' : '新建隧道'"
      width="680px"
      destroy-on-close
    >
      <el-form
        ref="formRef"
        :model="form"
        :rules="formRules"
        label-position="top"
        status-icon
      >
        <div class="management-form-grid">
          <el-form-item
            label="所属分组"
            prop="groupId"
          >
            <el-select
              v-model="form.groupId"
              placeholder="选择分组"
            >
              <el-option
                v-for="group in proxyGroups"
                :key="group.id"
                :label="group.name"
                :value="group.id"
              />
            </el-select>
          </el-form-item>

          <el-form-item
            label="隧道名称"
            prop="name"
          >
            <el-input
              v-model="form.name"
              maxlength="128"
              show-word-limit
              placeholder="例如：demo-tcp"
            />
          </el-form-item>

          <el-form-item label="协议">
            <el-segmented
              v-model="form.protocol"
              :options="[
                { label: 'TCP', value: 'tcp' },
                { label: 'UDP', value: 'udp' },
              ]"
            />
          </el-form-item>

          <el-form-item label="映射类型">
            <el-segmented
              v-model="form.remoteType"
              :options="[
                { label: '单端口', value: 'single' },
                { label: '端口范围', value: 'range' },
              ]"
            />
          </el-form-item>

          <el-form-item
            label="远端起始端口"
            prop="remoteStart"
          >
            <el-input-number
              v-model="form.remoteStart"
              :min="1"
              :max="65535"
              controls-position="right"
            />
          </el-form-item>

          <el-form-item
            label="远端结束端口"
            prop="remoteEnd"
          >
            <el-input-number
              v-model="form.remoteEnd"
              :min="1"
              :max="65535"
              :disabled="isSingleRemoteType"
              controls-position="right"
            />
          </el-form-item>

          <el-form-item
            label="本地地址"
            prop="localHost"
          >
            <el-input
              v-model="form.localHost"
              placeholder="例如：127.0.0.1"
            />
          </el-form-item>

          <el-form-item
            label="本地起始端口"
            prop="localStart"
          >
            <el-input-number
              v-model="form.localStart"
              :min="1"
              :max="65535"
              controls-position="right"
            />
          </el-form-item>

          <el-form-item
            label="本地结束端口"
            prop="localEnd"
          >
            <el-input-number
              v-model="form.localEnd"
              :min="1"
              :max="65535"
              :disabled="isSingleRemoteType"
              controls-position="right"
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
        </div>

        <p class="management-form-note">
          单端口模式会自动保持起始端口和结束端口一致；范围模式要求远端和本地端口跨度相同。
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
            {{ isEditing ? "保存变更" : "创建隧道" }}
          </el-button>
        </div>
      </template>
    </el-dialog>
  </section>
</template>
