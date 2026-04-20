<script setup lang="ts">
import { computed, onMounted, ref } from "vue";
import { storeToRefs } from "pinia";
import { RouterLink } from "vue-router";

import { fetchProxyGroups, fetchTunnels } from "@/api/management";
import { ApiError } from "@/api/http";
import { useAuthStore } from "@/stores/auth";

const knownErrorMessages: Record<string, string> = {
  "management session is invalid or expired": "管理会话已失效，请重新登录。",
  "management session is required": "需要先登录管理面。",
  "management store is unavailable": "管理存储当前不可用。",
};

const authStore = useAuthStore();
const { authenticated, expiresAt, initialized, ready } = storeToRefs(authStore);

const loading = ref(false);
const pageError = ref("");
const refreshedAt = ref("");
const proxyGroupTotal = ref(0);
const proxyGroupEnabled = ref(0);
const tunnelTotal = ref(0);
const tunnelEnabled = ref(0);
const tcpTunnelCount = ref(0);
const udpTunnelCount = ref(0);
const rangeTunnelCount = ref(0);

const proxyGroupDisabled = computed(() => Math.max(0, proxyGroupTotal.value - proxyGroupEnabled.value));
const tunnelDisabled = computed(() => Math.max(0, tunnelTotal.value - tunnelEnabled.value));
const singleTunnelCount = computed(() => Math.max(0, tunnelTotal.value - rangeTunnelCount.value));

const initializationLabel = computed(() => {
  if (!ready.value) {
    return "读取中";
  }
  if (initialized.value === null) {
    return "未返回";
  }
  return initialized.value ? "已初始化" : "等待初始化";
});

const initializationType = computed(() => {
  if (!ready.value) {
    return "info";
  }
  if (initialized.value === false) {
    return "warning";
  }
  return "success";
});

const sessionLabel = computed(() => {
  if (!ready.value) {
    return "读取中";
  }
  if (authenticated.value) {
    return "管理会话有效";
  }
  if (initialized.value === false) {
    return "尚未初始化";
  }
  return "尚未登录";
});

const sessionType = computed(() => {
  if (!ready.value) {
    return "info";
  }
  if (authenticated.value) {
    return "success";
  }
  if (initialized.value === false) {
    return "warning";
  }
  return "info";
});

const sessionExpiryLabel = computed(() => {
  if (!authenticated.value) {
    return "未登录";
  }
  return formatDateTime(expiresAt.value) || "未返回到期时间";
});

const refreshedAtLabel = computed(() => {
  if (loading.value && !refreshedAt.value) {
    return "首次读取中";
  }
  return formatDateTime(refreshedAt.value) || "尚未同步";
});

const proxyGroupSummary = computed(() => {
  if (proxyGroupTotal.value === 0) {
    return "当前没有分组";
  }
  return `启用 ${proxyGroupEnabled.value} / ${proxyGroupTotal.value}`;
});

const tunnelSummary = computed(() => {
  if (tunnelTotal.value === 0) {
    return "当前没有隧道";
  }
  return `启用 ${tunnelEnabled.value} / ${tunnelTotal.value}`;
});

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

function formatDateTime(value: string | null | undefined): string {
  if (!value) {
    return "";
  }

  const parsed = new Date(value);
  if (Number.isNaN(parsed.getTime())) {
    return value;
  }

  return parsed.toLocaleString("zh-CN", { hour12: false });
}

async function loadOverview(): Promise<void> {
  loading.value = true;

  try {
    const [proxyGroups, tunnels] = await Promise.all([fetchProxyGroups(), fetchTunnels()]);

    proxyGroupTotal.value = proxyGroups.length;
    proxyGroupEnabled.value = proxyGroups.filter((item) => item.enabled).length;

    tunnelTotal.value = tunnels.length;
    tunnelEnabled.value = tunnels.filter((item) => item.enabled).length;
    tcpTunnelCount.value = tunnels.filter((item) => item.protocol === "tcp").length;
    udpTunnelCount.value = tunnels.filter((item) => item.protocol === "udp").length;
    rangeTunnelCount.value = tunnels.filter((item) => item.remoteType === "range").length;

    refreshedAt.value = new Date().toISOString();
    pageError.value = "";
  } catch (error) {
    pageError.value = resolveMessage(error, "概览数据读取失败");
  } finally {
    loading.value = false;
  }
}

onMounted(() => {
  void loadOverview();
});
</script>

<template>
  <section class="page-stack dashboard-page">
    <div class="page-header">
      <div class="page-header__copy">
        <p class="page-section-label">current state</p>
        <h3 class="page-section-title">在一个页面里查看管理认证、分组与隧道的当前收口状态</h3>
        <p class="page-copy">
          首页只负责状态总览、关键入口和运行边界提示，不重复承接分组或隧道页内部的 CRUD 操作。当前界面只表达真实已接通的能力。
        </p>
        <p class="page-inline-note">最近同步：{{ refreshedAtLabel }}</p>
      </div>

      <div class="page-actions">
        <el-button
          plain
          :loading="loading"
          @click="loadOverview"
        >
          刷新概览
        </el-button>

        <RouterLink to="/proxy-groups">
          <el-button type="primary">
            进入分组管理
          </el-button>
        </RouterLink>

        <RouterLink to="/tunnels">
          <el-button plain>
            进入隧道管理
          </el-button>
        </RouterLink>
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
      <el-card class="metric-card dashboard-metric-card">
        <span class="metric-label">管理密钥状态</span>

        <div class="metric-value-row">
          <strong class="metric-value">{{ initializationLabel }}</strong>
          <el-tag
            size="small"
            effect="plain"
            :type="initializationType"
          >
            {{ ready ? "服务端状态已接通" : "正在读取状态" }}
          </el-tag>
        </div>

        <p class="metric-note">删除 <code>auth.json</code> 后，服务端会自动回到未初始化态。</p>
      </el-card>

      <el-card class="metric-card dashboard-metric-card">
        <span class="metric-label">管理会话状态</span>

        <div class="metric-value-row">
          <strong class="metric-value">{{ sessionLabel }}</strong>
          <el-tag
            size="small"
            effect="plain"
            :type="sessionType"
          >
            {{ authenticated ? "允许进入管理态" : "当前未持有会话" }}
          </el-tag>
        </div>

        <p class="metric-note">会话到期：{{ sessionExpiryLabel }}</p>
      </el-card>

      <el-card class="metric-card dashboard-metric-card">
        <span class="metric-label">分组概览</span>
        <strong class="metric-value">{{ proxyGroupTotal }}</strong>
        <p class="metric-note">
          {{ proxyGroupSummary }}，停用 {{ proxyGroupDisabled }} 个。分组 token 只在创建和重置时返回一次明文。
        </p>
      </el-card>

      <el-card class="metric-card dashboard-metric-card">
        <span class="metric-label">隧道概览</span>
        <strong class="metric-value">{{ tunnelTotal }}</strong>
        <p class="metric-note">
          {{ tunnelSummary }}，TCP {{ tcpTunnelCount }} / UDP {{ udpTunnelCount }}，范围映射 {{ rangeTunnelCount }} 个。
        </p>
      </el-card>
    </div>

    <div class="page-body">
      <div class="page-main">
        <el-card class="glass-panel page-section-card">
          <template #header>
            <div class="management-card-header">
              <div>
                <div class="panel-header">当前能力概览</div>
                <p class="management-card-copy">
                  首页不再讲“阶段”或“下一步计划”，只集中表达当前真实已接通的运行能力。
                </p>
              </div>
            </div>
          </template>

          <div class="capability-grid">
            <div class="capability-item">
              <span class="capability-item__eyebrow">management auth</span>
              <strong class="capability-item__title">初始化、登录与会话恢复</strong>
              <p class="capability-item__copy">
                管理密钥只初始化一次并持久化在服务端本地 <code>auth.json</code>；浏览器登录使用一次性 challenge proof。
              </p>
            </div>

            <div class="capability-item">
              <span class="capability-item__eyebrow">proxy groups</span>
              <strong class="capability-item__title">分组、启停状态与 token 生命周期</strong>
              <p class="capability-item__copy">
                当前可以直接创建、编辑、删除分组，并在创建或重置时领取一次性明文 token。
              </p>
            </div>

            <div class="capability-item">
              <span class="capability-item__eyebrow">tunnels</span>
              <strong class="capability-item__title">TCP / UDP 单端口与范围映射</strong>
              <p class="capability-item__copy">
                当前隧道管理已经接通 TCP / UDP 单端口映射与连续范围映射；范围模式要求远端和本地跨度一致。
              </p>
            </div>
          </div>
        </el-card>

        <el-card class="glass-panel page-section-card">
          <template #header>
            <div class="management-card-header">
              <div>
                <div class="panel-header">当前配置画像</div>
                <p class="management-card-copy">
                  下面的摘要直接来自现有认证状态、分组列表和隧道列表读取结果。
                </p>
              </div>
            </div>
          </template>

          <div class="overview-list">
            <div class="overview-row">
              <div class="overview-row__content">
                <span class="overview-row__label">管理认证</span>
                <p class="overview-row__copy">
                  管理密钥状态为“{{ initializationLabel }}”，当前会话状态为“{{ sessionLabel }}”。
                </p>
              </div>
              <strong class="overview-row__value">{{ sessionExpiryLabel }}</strong>
            </div>

            <div class="overview-row">
              <div class="overview-row__content">
                <span class="overview-row__label">分组运行面</span>
                <p class="overview-row__copy">
                  当前共有 {{ proxyGroupTotal }} 个分组，其中启用 {{ proxyGroupEnabled }} 个、停用 {{ proxyGroupDisabled }} 个。
                </p>
              </div>
              <strong class="overview-row__value">{{ proxyGroupSummary }}</strong>
            </div>

            <div class="overview-row">
              <div class="overview-row__content">
                <span class="overview-row__label">隧道运行面</span>
                <p class="overview-row__copy">
                  当前共有 {{ tunnelTotal }} 条隧道，其中启用 {{ tunnelEnabled }} 条、停用 {{ tunnelDisabled }} 条。
                </p>
              </div>
              <strong class="overview-row__value">TCP {{ tcpTunnelCount }} / UDP {{ udpTunnelCount }}</strong>
            </div>

            <div class="overview-row">
              <div class="overview-row__content">
                <span class="overview-row__label">映射结构</span>
                <p class="overview-row__copy">
                  单端口映射 {{ singleTunnelCount }} 条，范围映射 {{ rangeTunnelCount }} 条；创建隧道前必须先有可用分组。
                </p>
              </div>
              <strong class="overview-row__value">范围 {{ rangeTunnelCount }}</strong>
            </div>
          </div>
        </el-card>
      </div>

      <div class="page-side">
        <el-card class="glass-panel page-section-card">
          <template #header>
            <div class="management-card-header">
              <div>
                <div class="panel-header">快捷入口</div>
                <p class="management-card-copy">首页只保留当前真实存在且最常用的两个管理入口。</p>
              </div>
            </div>
          </template>

          <div class="quick-link-grid">
            <RouterLink
              class="quick-link-card"
              to="/proxy-groups"
            >
              <span class="quick-link-card__eyebrow">proxy groups</span>
              <strong class="quick-link-card__title">管理分组与 token</strong>
              <p class="quick-link-card__copy">
                进入分组列表，处理创建、启停、删除以及 token 重置。
              </p>
            </RouterLink>

            <RouterLink
              class="quick-link-card"
              to="/tunnels"
            >
              <span class="quick-link-card__eyebrow">tunnels</span>
              <strong class="quick-link-card__title">管理 TCP / UDP 映射</strong>
              <p class="quick-link-card__copy">
                进入隧道列表，管理单端口与连续范围映射配置。
              </p>
            </RouterLink>
          </div>
        </el-card>

        <el-card class="glass-panel page-section-card">
          <template #header>
            <div class="management-card-header">
              <div>
                <div class="panel-header">风险与边界</div>
                <p class="management-card-copy">这些约束会直接影响当前管理面的使用方式。</p>
              </div>
            </div>
          </template>

          <ul class="panel-list dashboard-risk-list">
            <li>当前管理面只覆盖概览、分组管理和隧道管理，不新增未来功能入口。</li>
            <li>服务端重启后，如果旧管理会话失效，需要重新登录管理面。</li>
            <li>删除 <code>auth.json</code> 会立即重置管理认证状态，并清空旧会话。</li>
            <li>分组 token 不会在列表页历史回显，创建和重置后需要立即保存。</li>
            <li>隧道页当前只支持 TCP / UDP 单端口和连续范围映射，不扩展到其他协议。</li>
          </ul>
        </el-card>
      </div>
    </div>
  </section>
</template>
