<script setup lang="ts">
import { computed, onMounted, reactive, ref, watch } from "vue";
import { ElMessage, ElMessageBox, type FormInstance, type FormRules } from "element-plus";

import {
  createProxyGroup,
  createTunnel,
  deleteProxyGroup,
  deleteTunnel,
  fetchProxyGroups,
  fetchTunnels,
  resetProxyGroupToken,
  updateProxyGroup,
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
  "name is required": "请输入名称。",
  "port range end must be greater than or equal to start": "结束端口不能小于起始端口。",
  "ports must be between 1 and 65535": "端口范围必须在 1 到 65535 之间。",
  "protocol must be tcp or udp": "协议只支持 TCP 或 UDP。",
  "proxy group name already exists": "分组名称已存在。",
  "proxy group not found": "分组不存在，列表将自动刷新。",
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

type TunnelStats = {
  total: number;
  enabled: number;
  tcp: number;
  udp: number;
  range: number;
  single: number;
};

const loading = ref(false);
const pageError = ref("");
const refreshedAt = ref("");
const proxyGroups = ref<ProxyGroup[]>([]);
const tunnels = ref<Tunnel[]>([]);
const selectedGroupId = ref<number | null>(null);

const groupDialogVisible = ref(false);
const groupSubmitting = ref(false);
const groupFormRef = ref<FormInstance>();
const editingGroupId = ref<number | null>(null);
const groupForm = reactive({
  name: "",
  enabled: true,
});

const tokenDialogVisible = ref(false);
const revealedToken = reactive({
  groupName: "",
  tokenId: "",
  token: "",
});

const tunnelDialogVisible = ref(false);
const tunnelSubmitting = ref(false);
const tunnelFormRef = ref<FormInstance>();
const editingTunnelId = ref<number | null>(null);
const tunnelForm = reactive<TunnelForm>({
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

const isEditingGroup = computed(() => editingGroupId.value !== null);
const isEditingTunnel = computed(() => editingTunnelId.value !== null);
const hasProxyGroups = computed(() => proxyGroups.value.length > 0);
const selectedGroup = computed(() => proxyGroups.value.find((item) => item.id === selectedGroupId.value) ?? null);
const refreshedAtLabel = computed(() => {
  if (loading.value && !refreshedAt.value) {
    return "首次读取中";
  }

  if (!refreshedAt.value) {
    return "尚未同步";
  }

  return formatDate(refreshedAt.value);
});
const totalGroupCount = computed(() => proxyGroups.value.length);
const enabledGroupCount = computed(() => proxyGroups.value.filter((item) => item.enabled).length);
const disabledGroupCount = computed(() => Math.max(0, totalGroupCount.value - enabledGroupCount.value));
const tunnelStatsByGroupId = computed(() => {
  const stats = new Map<number, TunnelStats>();

  for (const item of tunnels.value) {
    const entry =
      stats.get(item.groupId) ??
      {
        total: 0,
        enabled: 0,
        tcp: 0,
        udp: 0,
        range: 0,
        single: 0,
      };

    entry.total += 1;
    entry.enabled += item.enabled ? 1 : 0;
    entry.tcp += item.protocol === "tcp" ? 1 : 0;
    entry.udp += item.protocol === "udp" ? 1 : 0;
    entry.range += item.remoteType === "range" ? 1 : 0;
    entry.single += item.remoteType === "single" ? 1 : 0;
    stats.set(item.groupId, entry);
  }

  return stats;
});
const selectedGroupTunnels = computed(() => {
  if (selectedGroupId.value === null) {
    return [];
  }

  return tunnels.value.filter((item) => item.groupId === selectedGroupId.value);
});
const selectedTunnelStats = computed(() => {
  if (!selectedGroup.value) {
    return createEmptyTunnelStats();
  }

  return tunnelStatsByGroupId.value.get(selectedGroup.value.id) ?? createEmptyTunnelStats();
});
const selectedEnabledTunnelCount = computed(() => selectedTunnelStats.value.enabled);
const selectedDisabledTunnelCount = computed(() => Math.max(0, selectedTunnelStats.value.total - selectedTunnelStats.value.enabled));
const selectedGroupStateLabel = computed(() => {
  if (!selectedGroup.value) {
    return "未选择";
  }

  return selectedGroup.value.enabled ? "启用" : "停用";
});
const isSingleRemoteType = computed(() => tunnelForm.remoteType === "single");
const remotePortCount = computed(() => getPortCount(tunnelForm.remoteStart, tunnelForm.remoteEnd));
const localPortCount = computed(() => getPortCount(tunnelForm.localStart, tunnelForm.localEnd));
const tunnelFormModeLabel = computed(() => {
  return `${tunnelForm.protocol.toUpperCase()} · ${tunnelForm.remoteType === "single" ? "单端口映射" : "连续范围映射"}`;
});
const tunnelFormModeCopy = computed(() => {
  if (tunnelForm.remoteType === "single") {
    return "单端口模式下，起始端口变化时结束端口会自动保持一致。";
  }

  return "范围模式下，远端与本地端口范围必须保持相同跨度。";
});
const tunnelFormSpanTitle = computed(() => {
  if (tunnelForm.remoteType === "single") {
    return `远端 ${tunnelForm.remoteStart} -> ${tunnelForm.localHost}:${tunnelForm.localStart}`;
  }

  return `远端 ${remotePortCount.value} 个端口 / 本地 ${localPortCount.value} 个端口`;
});
const tunnelFormSpanCopy = computed(() => {
  if (tunnelForm.remoteType === "single") {
    return "保存后会将一个远端端口直接映射到一个本地目标端口。";
  }

  if (remotePortCount.value === localPortCount.value) {
    return "当前跨度一致，符合连续范围映射的保存条件。";
  }

  return "当前跨度不一致，提交前需要把远端和本地范围调整为相同长度。";
});

const groupFormRules: FormRules = {
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

const tunnelFormRules: FormRules = {
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
        if (value < tunnelForm.remoteStart) {
          callback(new Error("远端结束端口不能小于起始端口"));
          return;
        }
        if (tunnelForm.remoteType === "single" && value !== tunnelForm.remoteStart) {
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
        if (value < tunnelForm.localStart) {
          callback(new Error("本地结束端口不能小于起始端口"));
          return;
        }
        if (tunnelForm.remoteType === "single" && value !== tunnelForm.localStart) {
          callback(new Error("单端口映射时本地端口必须一致"));
          return;
        }
        if (tunnelForm.remoteType === "range") {
          const remoteSpan = tunnelForm.remoteEnd - tunnelForm.remoteStart;
          const localSpan = value - tunnelForm.localStart;
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

function createEmptyTunnelStats(): TunnelStats {
  return {
    total: 0,
    enabled: 0,
    tcp: 0,
    udp: 0,
    range: 0,
    single: 0,
  };
}

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

function isValidPort(value: number): boolean {
  return Number.isInteger(value) && value >= 1 && value <= 65535;
}

function getPortCount(start: number, end: number): number {
  if (!isValidPort(start) || !isValidPort(end) || end < start) {
    return 0;
  }

  return end - start + 1;
}

function formatEndpoint(item: Tunnel, side: "remote" | "local"): string {
  if (side === "remote") {
    return item.remoteType === "single" ? `${item.remoteStart}` : `${item.remoteStart}-${item.remoteEnd}`;
  }

  return item.remoteType === "single"
    ? `${item.localHost}:${item.localStart}`
    : `${item.localHost}:${item.localStart}-${item.localEnd}`;
}

function formatRemoteTypeLabel(value: TunnelRemoteType): string {
  return value === "single" ? "单端口" : "范围映射";
}

function formatRemoteTypeCopy(item: Tunnel): string {
  if (item.remoteType === "single") {
    return "单个远端端口映射到单个本地目标端口。";
  }

  return `共 ${getPortCount(item.remoteStart, item.remoteEnd)} 个连续端口，远端与本地跨度一致。`;
}

function formatLocalTargetCopy(item: Tunnel): string {
  if (item.remoteType === "single") {
    return "直接转发到当前本地目标。";
  }

  return `本地侧同样占用 ${getPortCount(item.localStart, item.localEnd)} 个连续端口。`;
}

function getTunnelStats(groupId: number): TunnelStats {
  return tunnelStatsByGroupId.value.get(groupId) ?? createEmptyTunnelStats();
}

function resolveSelectedGroup(groups: ProxyGroup[], preferredSelectedGroupId?: number | null): number | null {
  if (typeof preferredSelectedGroupId === "number" && groups.some((item) => item.id === preferredSelectedGroupId)) {
    return preferredSelectedGroupId;
  }

  if (selectedGroupId.value !== null && groups.some((item) => item.id === selectedGroupId.value)) {
    return selectedGroupId.value;
  }

  return groups[0]?.id ?? null;
}

function resolveDeleteFallbackGroupId(groupId: number): number | null {
  const currentIndex = proxyGroups.value.findIndex((item) => item.id === groupId);
  if (currentIndex === -1) {
    return proxyGroups.value[0]?.id ?? null;
  }

  return proxyGroups.value[currentIndex + 1]?.id ?? proxyGroups.value[currentIndex - 1]?.id ?? null;
}

async function loadManagementData(preferredSelectedGroupId?: number | null): Promise<void> {
  loading.value = true;

  try {
    const [groups, tunnelItems] = await Promise.all([fetchProxyGroups(), fetchTunnels()]);
    proxyGroups.value = groups;
    tunnels.value = tunnelItems;
    selectedGroupId.value = resolveSelectedGroup(groups, preferredSelectedGroupId);
    refreshedAt.value = new Date().toISOString();
    pageError.value = "";
  } catch (error) {
    pageError.value = resolveMessage(error, "管理数据读取失败");
  } finally {
    loading.value = false;
  }
}

async function refreshPage(): Promise<void> {
  await loadManagementData(selectedGroupId.value ?? undefined);
}

function handleGroupRowClick(item: ProxyGroup): void {
  selectedGroupId.value = item.id;
}

function resolveGroupRowClassName({ row }: { row: ProxyGroup }): string {
  return row.id === selectedGroupId.value ? "group-row--selected" : "";
}

function resetGroupForm(): void {
  groupForm.name = "";
  groupForm.enabled = true;
  groupFormRef.value?.clearValidate();
}

function openCreateGroupDialog(): void {
  editingGroupId.value = null;
  resetGroupForm();
  groupDialogVisible.value = true;
}

function openEditGroupDialog(item: ProxyGroup): void {
  editingGroupId.value = item.id;
  groupForm.name = item.name;
  groupForm.enabled = item.enabled;
  groupFormRef.value?.clearValidate();
  groupDialogVisible.value = true;
}

function openEditSelectedGroupDialog(): void {
  if (!selectedGroup.value) {
    return;
  }

  openEditGroupDialog(selectedGroup.value);
}

function showTokenDialog(item: ProxyGroup, token: string): void {
  revealedToken.groupName = item.name;
  revealedToken.tokenId = item.tokenId;
  revealedToken.token = token;
  tokenDialogVisible.value = true;
}

async function handleGroupSubmit(): Promise<void> {
  if (!groupFormRef.value) {
    return;
  }

  const valid = await groupFormRef.value.validate().catch(() => false);
  if (!valid) {
    return;
  }

  groupSubmitting.value = true;

  try {
    const payload = {
      name: groupForm.name.trim(),
      enabled: groupForm.enabled,
    };

    if (isEditingGroup.value && editingGroupId.value !== null) {
      const item = await updateProxyGroup(editingGroupId.value, payload);
      await loadManagementData(item.id);
      groupDialogVisible.value = false;
      ElMessage.success(`分组“${item.name}”已更新。`);
      return;
    }

    const created = await createProxyGroup(payload);
    await loadManagementData(created.item.id);
    groupDialogVisible.value = false;
    showTokenDialog(created.item, created.token);
    ElMessage.success(`分组“${created.item.name}”已创建。`);
  } catch (error) {
    ElMessage.error(resolveMessage(error, "分组保存失败"));
  } finally {
    groupSubmitting.value = false;
  }
}

async function handleGroupDelete(item: ProxyGroup): Promise<void> {
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

  const preferredGroupId = item.id === selectedGroupId.value ? resolveDeleteFallbackGroupId(item.id) : selectedGroupId.value;

  try {
    await deleteProxyGroup(item.id);
    await loadManagementData(preferredGroupId ?? undefined);
    ElMessage.success(`分组“${item.name}”已删除。`);
  } catch (error) {
    ElMessage.error(resolveMessage(error, "分组删除失败"));
  }
}

async function handleResetGroupToken(item: ProxyGroup): Promise<void> {
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
    await loadManagementData(result.item.id);
    showTokenDialog(result.item, result.token);
    ElMessage.success(`分组“${result.item.name}”的 token 已重置。`);
  } catch (error) {
    ElMessage.error(resolveMessage(error, "分组 token 重置失败"));
  }
}

async function handleResetSelectedGroupToken(): Promise<void> {
  if (!selectedGroup.value) {
    return;
  }

  await handleResetGroupToken(selectedGroup.value);
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

function resolveTunnelGroupName(groupId: number): string {
  return proxyGroups.value.find((item) => item.id === groupId)?.name ?? "未选择分组";
}

function resetTunnelForm(groupId?: number): void {
  tunnelForm.groupId = groupId ?? selectedGroup.value?.id ?? proxyGroups.value[0]?.id ?? 0;
  tunnelForm.name = "";
  tunnelForm.protocol = "tcp";
  tunnelForm.remoteType = "single";
  tunnelForm.remoteStart = 7000;
  tunnelForm.remoteEnd = 7000;
  tunnelForm.localHost = "127.0.0.1";
  tunnelForm.localStart = 7000;
  tunnelForm.localEnd = 7000;
  tunnelForm.enabled = true;
  tunnelFormRef.value?.clearValidate();
}

function openCreateTunnelDialog(): void {
  if (!selectedGroup.value) {
    ElMessage.warning("请先创建并选中分组，再创建隧道。");
    return;
  }

  editingTunnelId.value = null;
  resetTunnelForm(selectedGroup.value.id);
  tunnelDialogVisible.value = true;
}

function openEditTunnelDialog(item: Tunnel): void {
  editingTunnelId.value = item.id;
  tunnelForm.groupId = item.groupId;
  tunnelForm.name = item.name;
  tunnelForm.protocol = item.protocol;
  tunnelForm.remoteType = item.remoteType;
  tunnelForm.remoteStart = item.remoteStart;
  tunnelForm.remoteEnd = item.remoteEnd;
  tunnelForm.localHost = item.localHost;
  tunnelForm.localStart = item.localStart;
  tunnelForm.localEnd = item.localEnd;
  tunnelForm.enabled = item.enabled;
  tunnelFormRef.value?.clearValidate();
  tunnelDialogVisible.value = true;
}

async function handleTunnelSubmit(): Promise<void> {
  if (!tunnelFormRef.value) {
    return;
  }

  const valid = await tunnelFormRef.value.validate().catch(() => false);
  if (!valid) {
    return;
  }

  tunnelSubmitting.value = true;

  try {
    const payload = {
      groupId: tunnelForm.groupId,
      name: tunnelForm.name.trim(),
      protocol: tunnelForm.protocol,
      remoteType: tunnelForm.remoteType,
      remoteStart: tunnelForm.remoteStart,
      remoteEnd: tunnelForm.remoteEnd,
      localHost: tunnelForm.localHost.trim(),
      localStart: tunnelForm.localStart,
      localEnd: tunnelForm.localEnd,
      enabled: tunnelForm.enabled,
    };

    if (isEditingTunnel.value && editingTunnelId.value !== null) {
      const item = await updateTunnel(editingTunnelId.value, payload);
      await loadManagementData(item.groupId);
      tunnelDialogVisible.value = false;
      ElMessage.success(`隧道“${item.name}”已更新。`);
      return;
    }

    const item = await createTunnel(payload);
    await loadManagementData(item.groupId);
    tunnelDialogVisible.value = false;
    ElMessage.success(`隧道“${item.name}”已创建。`);
  } catch (error) {
    ElMessage.error(resolveMessage(error, "隧道保存失败"));
  } finally {
    tunnelSubmitting.value = false;
  }
}

async function handleTunnelDelete(item: Tunnel): Promise<void> {
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
    await loadManagementData(item.groupId);
    ElMessage.success(`隧道“${item.name}”已删除。`);
  } catch (error) {
    ElMessage.error(resolveMessage(error, "隧道删除失败"));
  }
}

watch(
  () => tunnelForm.remoteType,
  (value) => {
    if (value === "single") {
      tunnelForm.remoteEnd = tunnelForm.remoteStart;
      tunnelForm.localEnd = tunnelForm.localStart;
    }
  },
);

watch(
  () => tunnelForm.remoteStart,
  (value) => {
    if (tunnelForm.remoteType === "single") {
      tunnelForm.remoteEnd = value;
    }
  },
);

watch(
  () => tunnelForm.localStart,
  (value) => {
    if (tunnelForm.remoteType === "single") {
      tunnelForm.localEnd = value;
    }
  },
);

onMounted(() => {
  void loadManagementData();
});
</script>

<template>
  <section class="page-stack access-management-page">
    <div class="page-header">
      <div class="page-header__copy">
        <p class="page-section-label">access management</p>
        <h3 class="page-section-title">分组与隧道统一管理</h3>
        <p class="page-inline-note">
          当前页先维护接入分组，再在下方直接管理当前选中分组的隧道配置。最近同步：{{ refreshedAtLabel }}。
        </p>
      </div>

      <div class="page-actions access-page-actions">
        <el-button
          plain
          :loading="loading"
          @click="refreshPage"
        >
          刷新列表
        </el-button>
        <el-button
          type="primary"
          plain
          @click="openCreateGroupDialog"
        >
          新建分组
        </el-button>
        <el-button
          type="primary"
          :disabled="!selectedGroup"
          @click="openCreateTunnelDialog"
        >
          新建隧道
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
        <strong class="metric-value">{{ totalGroupCount }}</strong>
        <p class="metric-note">当前所有 frpc 接入和隧道归属都从这里建立。</p>
      </el-card>

      <el-card class="metric-card">
        <span class="metric-label">启用分组</span>
        <strong class="metric-value">{{ enabledGroupCount }}</strong>
        <p class="metric-note">启用 {{ enabledGroupCount }} 个，停用 {{ disabledGroupCount }} 个。</p>
      </el-card>

      <el-card class="metric-card">
        <span class="metric-label">当前分组</span>
        <strong class="metric-value metric-value--compact">{{ selectedGroup?.name ?? "未选择" }}</strong>
        <p class="metric-note">
          {{ selectedGroup ? `Token ID：${selectedGroup.tokenId}` : "当前还没有分组，需先创建接入容器。" }}
        </p>
      </el-card>

      <el-card class="metric-card">
        <span class="metric-label">当前分组隧道</span>
        <strong class="metric-value">{{ selectedTunnelStats.total }}</strong>
        <p class="metric-note">
          {{ selectedGroup ? `TCP ${selectedTunnelStats.tcp} / UDP ${selectedTunnelStats.udp}` : "选中分组后在下方查看隧道。" }}
        </p>
      </el-card>
    </div>

    <el-card class="glass-panel page-section-card access-section-card">
      <template #header>
        <div class="management-card-header">
          <div>
            <div class="panel-header">分组列表</div>
            <p class="management-card-copy">点击分组行切换下方隧道上下文；列表只展示当前真实存在的 token_id，不回显历史 token 明文。</p>
          </div>
        </div>
      </template>

      <el-empty
        v-if="!loading && !hasProxyGroups"
        description="当前还没有分组，先创建第一个接入分组。"
      >
        <el-button
          type="primary"
          @click="openCreateGroupDialog"
        >
          创建分组
        </el-button>
      </el-empty>

      <div
        v-else
        class="management-table-wrap"
      >
        <el-table
          v-loading="loading"
          class="management-data-table management-group-table"
          :data="proxyGroups"
          row-key="id"
          stripe
          :row-class-name="resolveGroupRowClassName"
          @row-click="handleGroupRowClick"
        >
          <el-table-column
            label="分组"
            min-width="230"
          >
            <template #default="{ row }">
              <div class="entity-stack">
                <div class="access-group-name">
                  <strong class="entity-stack__title">{{ row.name }}</strong>
                  <el-tag
                    v-if="row.id === selectedGroupId"
                    size="small"
                    type="primary"
                    effect="plain"
                  >
                    当前上下文
                  </el-tag>
                </div>
                <span class="entity-stack__meta">ID #{{ row.id }}</span>
              </div>
            </template>
          </el-table-column>

          <el-table-column
            label="Token ID"
            min-width="250"
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
            label="隧道"
            min-width="150"
          >
            <template #default="{ row }">
              <div class="access-table-count">
                <strong class="access-table-count__value">{{ getTunnelStats(row.id).total }}</strong>
                <span class="access-table-count__meta">启用 {{ getTunnelStats(row.id).enabled }} / TCP {{ getTunnelStats(row.id).tcp }}</span>
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
                  @click="openEditGroupDialog(row)"
                >
                  编辑
                </el-button>
                <el-button
                  link
                  type="warning"
                  @click="handleResetGroupToken(row)"
                >
                  重置 Token
                </el-button>
                <el-button
                  link
                  type="danger"
                  @click="handleGroupDelete(row)"
                >
                  删除
                </el-button>
              </div>
            </template>
          </el-table-column>
        </el-table>
      </div>
    </el-card>

    <el-card class="glass-panel page-section-card access-section-card">
      <template #header>
        <div class="management-card-header">
          <div>
            <div class="panel-header">当前分组隧道</div>
            <p class="management-card-copy">
              {{
                selectedGroup
                  ? `当前只展示分组“${selectedGroup.name}”下的隧道配置；新建隧道会自动绑定到该分组。`
                  : "当前还没有分组，先创建分组后再在这里维护对应隧道。"
              }}
            </p>
          </div>

          <div class="access-section-actions">
            <el-button
              plain
              :disabled="!selectedGroup"
              @click="openEditSelectedGroupDialog"
            >
              编辑当前分组
            </el-button>
            <el-button
              plain
              :disabled="!selectedGroup"
              @click="handleResetSelectedGroupToken"
            >
              重置 Token
            </el-button>
            <el-button
              type="primary"
              :disabled="!selectedGroup"
              @click="openCreateTunnelDialog"
            >
              新建隧道
            </el-button>
          </div>
        </div>
      </template>

      <div
        v-if="selectedGroup"
        class="access-selected-banner"
      >
        <div class="access-selected-banner__item">
          <span class="page-section-label">group</span>
          <strong>{{ selectedGroup.name }}</strong>
          <p class="access-selected-banner__meta">Token ID：<code>{{ selectedGroup.tokenId }}</code></p>
        </div>

        <div class="access-selected-banner__item">
          <span class="page-section-label">state</span>
          <strong>{{ selectedGroupStateLabel }}</strong>
          <p class="access-selected-banner__meta">
            客户端：{{ formatAccessMode(selectedGroup.clientAccessMode) }} / 隧道入口：{{ formatAccessMode(selectedGroup.tunnelAccessMode) }}
          </p>
        </div>

        <div class="access-selected-banner__item">
          <span class="page-section-label">tunnels</span>
          <strong>{{ selectedTunnelStats.total }}</strong>
          <p class="access-selected-banner__meta">启用 {{ selectedEnabledTunnelCount }} / 停用 {{ selectedDisabledTunnelCount }}</p>
        </div>

        <div class="access-selected-banner__item">
          <span class="page-section-label">mix</span>
          <strong>TCP {{ selectedTunnelStats.tcp }} / UDP {{ selectedTunnelStats.udp }}</strong>
          <p class="access-selected-banner__meta">
            单端口 {{ selectedTunnelStats.single }} / 范围 {{ selectedTunnelStats.range }}
          </p>
        </div>
      </div>

      <el-empty
        v-if="!loading && !selectedGroup"
        description="当前还没有分组，先创建分组后再配置隧道。"
      >
        <el-button
          type="primary"
          @click="openCreateGroupDialog"
        >
          创建分组
        </el-button>
      </el-empty>

      <el-empty
        v-else-if="!loading && selectedGroupTunnels.length === 0"
        :description="`分组“${selectedGroup?.name ?? ''}”下还没有隧道映射。`"
      >
        <el-button
          type="primary"
          @click="openCreateTunnelDialog"
        >
          创建第一条隧道
        </el-button>
      </el-empty>

      <div
        v-else
        class="management-table-wrap"
      >
        <el-table
          v-loading="loading"
          class="management-data-table management-tunnel-table"
          :data="selectedGroupTunnels"
          stripe
        >
          <el-table-column
            label="隧道"
            min-width="230"
          >
            <template #default="{ row }">
              <div class="entity-stack">
                <strong class="entity-stack__title">{{ row.name }}</strong>
                <span class="entity-stack__meta">ID #{{ row.id }}</span>
              </div>
            </template>
          </el-table-column>

          <el-table-column
            label="协议 / 模式"
            min-width="170"
          >
            <template #default="{ row }">
              <div class="tunnel-badge-stack">
                <el-tag :type="row.protocol === 'tcp' ? 'primary' : 'success'">
                  {{ row.protocol.toUpperCase() }}
                </el-tag>
                <el-tag
                  type="info"
                  effect="plain"
                >
                  {{ formatRemoteTypeLabel(row.remoteType) }}
                </el-tag>
              </div>
            </template>
          </el-table-column>

          <el-table-column
            label="远端入口"
            min-width="210"
          >
            <template #default="{ row }">
              <div class="entity-stack entity-stack--tight">
                <strong class="entity-stack__title tunnel-table-endpoint">{{ formatEndpoint(row, "remote") }}</strong>
                <span class="entity-stack__meta">{{ formatRemoteTypeCopy(row) }}</span>
              </div>
            </template>
          </el-table-column>

          <el-table-column
            label="本地目标"
            min-width="260"
          >
            <template #default="{ row }">
              <div class="entity-stack entity-stack--tight">
                <strong class="entity-stack__title tunnel-table-endpoint">{{ formatEndpoint(row, "local") }}</strong>
                <span class="entity-stack__meta">{{ formatLocalTargetCopy(row) }}</span>
              </div>
            </template>
          </el-table-column>

          <el-table-column
            label="状态 / 时间"
            min-width="200"
          >
            <template #default="{ row }">
              <div class="entity-stack entity-stack--tight">
                <div class="tunnel-badge-stack">
                  <el-tag :type="row.enabled ? 'success' : 'info'">
                    {{ row.enabled ? "启用" : "停用" }}
                  </el-tag>
                </div>
                <span class="entity-stack__meta">创建：{{ formatDate(row.createdAt) }}</span>
                <span class="entity-stack__meta">更新：{{ formatDate(row.updatedAt) }}</span>
              </div>
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
                  @click="openEditTunnelDialog(row)"
                >
                  编辑
                </el-button>
                <el-button
                  link
                  type="danger"
                  @click="handleTunnelDelete(row)"
                >
                  删除
                </el-button>
              </div>
            </template>
          </el-table-column>
        </el-table>
      </div>
    </el-card>

    <el-dialog
      v-model="groupDialogVisible"
      :title="isEditingGroup ? '编辑分组' : '新建分组'"
      width="520px"
      destroy-on-close
    >
      <el-form
        ref="groupFormRef"
        :model="groupForm"
        :rules="groupFormRules"
        label-position="top"
        status-icon
      >
        <el-form-item
          label="分组名称"
          prop="name"
        >
          <el-input
            v-model="groupForm.name"
            maxlength="128"
            show-word-limit
            placeholder="例如：default-group"
          />
        </el-form-item>

        <el-form-item label="启用状态">
          <el-switch
            v-model="groupForm.enabled"
            inline-prompt
            active-text="启用"
            inactive-text="停用"
          />
        </el-form-item>

        <p class="management-form-note">
          {{
            isEditingGroup
              ? "编辑分组不会返回新的 token；如需替换 frpc 凭据，请执行“重置 Token”。"
              : "创建分组后会返回一次性明文 token，请在弹窗中立即保存给对应 frpc 使用。"
          }}
        </p>
      </el-form>

      <template #footer>
        <div class="management-dialog-actions">
          <el-button @click="groupDialogVisible = false">
            取消
          </el-button>
          <el-button
            type="primary"
            :loading="groupSubmitting"
            @click="handleGroupSubmit"
          >
            {{ isEditingGroup ? "保存变更" : "创建分组" }}
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

    <el-dialog
      v-model="tunnelDialogVisible"
      :title="isEditingTunnel ? '编辑隧道' : '新建隧道'"
      width="760px"
      destroy-on-close
    >
      <el-form
        ref="tunnelFormRef"
        :model="tunnelForm"
        :rules="tunnelFormRules"
        label-position="top"
        status-icon
      >
        <div class="tunnel-form-summary">
          <div class="tunnel-form-summary__item">
            <span class="page-section-label">current mode</span>
            <strong>{{ tunnelFormModeLabel }}</strong>
            <p class="management-form-note">{{ tunnelFormModeCopy }}</p>
          </div>

          <div class="tunnel-form-summary__item">
            <span class="page-section-label">range rule</span>
            <strong>{{ tunnelFormSpanTitle }}</strong>
            <p class="management-form-note">{{ tunnelFormSpanCopy }}</p>
          </div>
        </div>

        <div class="management-form-grid">
          <el-form-item
            class="management-form-grid__span-2"
            label="所属分组"
            prop="groupId"
          >
            <el-input
              :model-value="resolveTunnelGroupName(tunnelForm.groupId)"
              disabled
            />
          </el-form-item>

          <el-form-item
            label="隧道名称"
            prop="name"
          >
            <el-input
              v-model="tunnelForm.name"
              maxlength="128"
              show-word-limit
              placeholder="例如：demo-tcp"
            />
          </el-form-item>

          <el-form-item label="协议">
            <el-segmented
              v-model="tunnelForm.protocol"
              :options="[
                { label: 'TCP', value: 'tcp' },
                { label: 'UDP', value: 'udp' },
              ]"
            />
          </el-form-item>

          <el-form-item label="映射类型">
            <el-segmented
              v-model="tunnelForm.remoteType"
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
              v-model="tunnelForm.remoteStart"
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
              v-model="tunnelForm.remoteEnd"
              :min="1"
              :max="65535"
              :disabled="isSingleRemoteType"
              controls-position="right"
            />
          </el-form-item>

          <el-form-item
            class="management-form-grid__span-2"
            label="本地地址"
            prop="localHost"
          >
            <el-input
              v-model="tunnelForm.localHost"
              placeholder="例如：127.0.0.1"
            />
          </el-form-item>

          <el-form-item
            label="本地起始端口"
            prop="localStart"
          >
            <el-input-number
              v-model="tunnelForm.localStart"
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
              v-model="tunnelForm.localEnd"
              :min="1"
              :max="65535"
              :disabled="isSingleRemoteType"
              controls-position="right"
            />
          </el-form-item>

          <el-form-item
            class="management-form-grid__span-2"
            label="启用状态"
          >
            <el-switch
              v-model="tunnelForm.enabled"
              inline-prompt
              active-text="启用"
              inactive-text="停用"
            />
          </el-form-item>
        </div>

        <p class="management-form-note">
          当前远端跨度 {{ remotePortCount }}，本地跨度 {{ localPortCount }}。范围模式下两者必须一致；单端口模式会自动收敛为 1 对 1 映射。
        </p>
      </el-form>

      <template #footer>
        <div class="management-dialog-actions">
          <el-button @click="tunnelDialogVisible = false">
            取消
          </el-button>
          <el-button
            type="primary"
            :loading="tunnelSubmitting"
            @click="handleTunnelSubmit"
          >
            {{ isEditingTunnel ? "保存变更" : "创建隧道" }}
          </el-button>
        </div>
      </template>
    </el-dialog>
  </section>
</template>
