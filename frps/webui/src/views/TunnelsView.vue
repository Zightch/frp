<script setup lang="ts">
import { computed, onMounted, reactive, ref, watch } from "vue";
import { ElMessage, ElMessageBox, type FormInstance, type FormRules } from "element-plus";
import { RouterLink } from "vue-router";

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
const refreshedAt = ref("");
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
const disabledCount = computed(() => Math.max(0, totalCount.value - enabledCount.value));
const rangeCount = computed(() => tunnels.value.filter((item) => item.remoteType === "range").length);
const singleCount = computed(() => Math.max(0, totalCount.value - rangeCount.value));
const tcpCount = computed(() => tunnels.value.filter((item) => item.protocol === "tcp").length);
const udpCount = computed(() => tunnels.value.filter((item) => item.protocol === "udp").length);
const isSingleRemoteType = computed(() => form.remoteType === "single");
const remotePortCount = computed(() => getPortCount(form.remoteStart, form.remoteEnd));
const localPortCount = computed(() => getPortCount(form.localStart, form.localEnd));
const latestUpdatedTunnel = computed(() => {
  let latest: Tunnel | null = null;
  let latestTimestamp = -1;

  for (const item of tunnels.value) {
    const timestamp = resolveTimestamp(item.updatedAt) ?? resolveTimestamp(item.createdAt) ?? -1;
    if (timestamp > latestTimestamp) {
      latest = item;
      latestTimestamp = timestamp;
    }
  }

  return latest;
});
const latestUpdatedTunnelName = computed(() => latestUpdatedTunnel.value?.name ?? "暂无隧道");
const latestUpdatedAtLabel = computed(() => {
  if (!latestUpdatedTunnel.value) {
    return "暂无更新";
  }

  return formatDate(latestUpdatedTunnel.value.updatedAt || latestUpdatedTunnel.value.createdAt);
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
const formModeLabel = computed(() => {
  return `${form.protocol.toUpperCase()} · ${form.remoteType === "single" ? "单端口映射" : "连续范围映射"}`;
});
const formModeCopy = computed(() => {
  if (form.remoteType === "single") {
    return "单端口模式下，起始端口变化时结束端口会自动保持一致。";
  }

  return "范围模式下，远端与本地端口范围必须保持相同跨度。";
});
const formSpanTitle = computed(() => {
  if (form.remoteType === "single") {
    return `远端 ${form.remoteStart} -> ${form.localHost}:${form.localStart}`;
  }

  return `远端 ${remotePortCount.value} 个端口 / 本地 ${localPortCount.value} 个端口`;
});
const formSpanCopy = computed(() => {
  if (form.remoteType === "single") {
    return "保存后会将一个远端端口直接映射到一个本地目标端口。";
  }

  if (remotePortCount.value === localPortCount.value) {
    return "当前跨度一致，符合连续范围映射的保存条件。";
  }

  return "当前跨度不一致，提交前需要把远端和本地范围调整为相同长度。";
});

function isValidPort(value: number): boolean {
  return Number.isInteger(value) && value >= 1 && value <= 65535;
}

function getPortCount(start: number, end: number): number {
  if (!isValidPort(start) || !isValidPort(end) || end < start) {
    return 0;
  }

  return end - start + 1;
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
    refreshedAt.value = new Date().toISOString();
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
  <section class="page-stack tunnels-page">
    <div class="page-header">
      <div class="page-header__copy">
        <p class="page-section-label">port mapping</p>
        <h3 class="page-section-title">统一管理 TCP / UDP 单端口映射与连续范围映射</h3>
        <p class="page-copy">
          隧道必须归属于已存在分组。当前页面只承接 TCP / UDP、单端口 / 范围映射这四类真实组合，不扩展到其他协议或额外入口模型。
        </p>
        <p class="page-inline-note">最近同步：{{ refreshedAtLabel }}。如果当前没有分组，需要先进入分组管理创建接入容器。</p>
      </div>

      <div class="page-actions">
        <el-button
          plain
          :loading="loading"
          @click="loadPage"
        >
          刷新列表
        </el-button>
        <el-button
          type="primary"
          :disabled="!hasProxyGroups"
          @click="openCreateDialog"
        >
          新建隧道
        </el-button>
      </div>
    </div>

    <div
      v-if="pageError || !hasProxyGroups"
      class="page-feedback"
    >
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
        title="当前还没有分组，请先到分组管理页创建分组后再配置隧道。"
      />
    </div>

    <div class="page-metrics">
      <el-card class="metric-card">
        <span class="metric-label">隧道总数</span>
        <strong class="metric-value">{{ totalCount }}</strong>
        <p class="metric-note">
          当前共读取到 {{ totalCount }} 条映射，归属于 {{ proxyGroups.length }} 个可选分组。
        </p>
      </el-card>

      <el-card class="metric-card">
        <span class="metric-label">启用隧道</span>
        <strong class="metric-value">{{ enabledCount }}</strong>
        <p class="metric-note">已启用 {{ enabledCount }} 条，停用 {{ disabledCount }} 条。</p>
      </el-card>

      <el-card class="metric-card">
        <span class="metric-label">协议分布</span>
        <strong class="metric-value">TCP {{ tcpCount }} / UDP {{ udpCount }}</strong>
        <p class="metric-note">当前隧道页只支持 TCP 与 UDP 两种协议。</p>
      </el-card>

      <el-card class="metric-card">
        <span class="metric-label">映射结构</span>
        <strong class="metric-value">单端口 {{ singleCount }} / 范围 {{ rangeCount }}</strong>
        <p class="metric-note">范围映射要求远端与本地使用相同跨度的连续端口段。</p>
      </el-card>
    </div>

    <div class="page-body">
      <div class="page-main">
        <el-card class="glass-panel page-section-card">
          <template #header>
            <div class="management-card-header">
              <div>
                <div class="panel-header">隧道列表</div>
                <p class="management-card-copy">
                  列表同时展示所属分组、协议、映射模式、远端入口和本地目标，便于直接核对当前端口占用关系。
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
            v-if="!loading && !hasProxyGroups"
            description="当前还没有可用于隧道的分组。"
          >
            <RouterLink to="/proxy-groups">
              <el-button type="primary">
                进入分组管理
              </el-button>
            </RouterLink>
          </el-empty>

          <el-empty
            v-else-if="!loading && tunnels.length === 0"
            description="当前还没有隧道映射，可以先创建第一条 TCP 或 UDP 规则。"
          >
            <el-button
              type="primary"
              @click="openCreateDialog"
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
              class="management-data-table"
              :data="tunnels"
              stripe
            >
              <el-table-column
                label="隧道"
                min-width="230"
              >
                <template #default="{ row }">
                  <div class="entity-stack">
                    <strong class="entity-stack__title">{{ row.name }}</strong>
                    <span class="entity-stack__meta">分组：{{ row.groupName }}</span>
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
          </div>
        </el-card>
      </div>

      <div class="page-side">
        <el-card class="glass-panel page-section-card">
          <template #header>
            <div class="management-card-header">
              <div>
                <div class="panel-header">当前规则</div>
                <p class="management-card-copy">右侧只总结当前服务端已经真实生效的隧道约束。</p>
              </div>
            </div>
          </template>

          <ul class="panel-list">
            <li>创建隧道前必须先有分组，所有隧道都只能归属于已存在的分组。</li>
            <li>当前协议只支持 TCP 与 UDP，不扩展到其他代理协议。</li>
            <li>单端口模式会自动保持起始端口和结束端口一致。</li>
            <li>范围模式要求远端和本地端口范围跨度完全一致，并且都必须是连续端口段。</li>
            <li>同一分组内的隧道名称必须唯一，删除后映射会立即失效。</li>
          </ul>
        </el-card>

        <el-card class="glass-panel page-section-card">
          <template #header>
            <div class="management-card-header">
              <div>
                <div class="panel-header">当前画像</div>
                <p class="management-card-copy">所有摘要都直接来自当前读到的分组与隧道列表。</p>
              </div>
            </div>
          </template>

          <div class="overview-list">
            <div class="overview-row">
              <div class="overview-row__content">
                <span class="overview-row__label">最近同步</span>
                <p class="overview-row__copy">当前管理会话最近一次重新拉取隧道与分组列表的时间。</p>
              </div>
              <strong class="overview-row__value">{{ refreshedAtLabel }}</strong>
            </div>

            <div class="overview-row">
              <div class="overview-row__content">
                <span class="overview-row__label">分组就绪度</span>
                <p class="overview-row__copy">
                  {{
                    hasProxyGroups
                      ? `当前共有 ${proxyGroups.length} 个可选分组，可直接用于新建隧道。`
                      : "当前没有分组，因此无法创建任何新的隧道规则。"
                  }}
                </p>
              </div>
              <strong class="overview-row__value">{{ hasProxyGroups ? `${proxyGroups.length} 个可选` : "需先建组" }}</strong>
            </div>

            <div class="overview-row">
              <div class="overview-row__content">
                <span class="overview-row__label">协议分布</span>
                <p class="overview-row__copy">当前隧道总量中 TCP 与 UDP 的实际占比分布。</p>
              </div>
              <strong class="overview-row__value">TCP {{ tcpCount }} / UDP {{ udpCount }}</strong>
            </div>

            <div class="overview-row">
              <div class="overview-row__content">
                <span class="overview-row__label">映射结构</span>
                <p class="overview-row__copy">
                  单端口 {{ singleCount }} 条，范围映射 {{ rangeCount }} 条；范围模式只允许连续端口段。
                </p>
              </div>
              <strong class="overview-row__value">{{ rangeCount }} 范围</strong>
            </div>

            <div class="overview-row">
              <div class="overview-row__content">
                <span class="overview-row__label">最近更新</span>
                <p class="overview-row__copy">最近一次创建或编辑对应到当前列表中的最新更新时间。</p>
              </div>
              <strong class="overview-row__value">{{ latestUpdatedTunnelName }}</strong>
            </div>
          </div>
        </el-card>
      </div>
    </div>

    <el-dialog
      v-model="dialogVisible"
      :title="isEditing ? '编辑隧道' : '新建隧道'"
      width="760px"
      destroy-on-close
    >
      <el-form
        ref="formRef"
        :model="form"
        :rules="formRules"
        label-position="top"
        status-icon
      >
        <div class="tunnel-form-summary">
          <div class="tunnel-form-summary__item">
            <span class="page-section-label">current mode</span>
            <strong>{{ formModeLabel }}</strong>
            <p class="management-form-note">{{ formModeCopy }}</p>
          </div>

          <div class="tunnel-form-summary__item">
            <span class="page-section-label">range rule</span>
            <strong>{{ formSpanTitle }}</strong>
            <p class="management-form-note">{{ formSpanCopy }}</p>
          </div>
        </div>

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
            class="management-form-grid__span-2"
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

          <el-form-item
            class="management-form-grid__span-2"
            label="启用状态"
          >
            <el-switch
              v-model="form.enabled"
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
