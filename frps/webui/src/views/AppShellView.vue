<script setup lang="ts">
import { computed } from "vue";
import { storeToRefs } from "pinia";
import { ElMessage } from "element-plus";
import { useRouter } from "vue-router";

import { useAuthStore } from "@/stores/auth";

const router = useRouter();
const authStore = useAuthStore();
const { authenticated, expiresAt, initialized, lastError, loading, loggingOut, ready } = storeToRefs(authStore);

const authStatusLabel = computed(() => {
  if (!ready.value || loading.value) {
    return "认证状态读取中";
  }
  if (initialized.value === false) {
    return "等待初始化";
  }
  if (authenticated.value) {
    return "管理会话有效";
  }
  return "尚未登录";
});

const authStatusType = computed(() => {
  if (!ready.value || loading.value) {
    return "info";
  }
  if (initialized.value === false) {
    return "warning";
  }
  if (authenticated.value) {
    return "success";
  }
  return "info";
});

const formattedExpiry = computed(() => {
  if (!expiresAt.value) {
    return "";
  }

  const date = new Date(expiresAt.value);
  if (Number.isNaN(date.getTime())) {
    return expiresAt.value;
  }

  return date.toLocaleString("zh-CN", {
    hour12: false,
  });
});

async function handleRefresh(): Promise<void> {
  await authStore.bootstrap(true);
}

async function handleLogout(): Promise<void> {
  await authStore.logout();
  ElMessage.success("已退出管理会话。");
  await router.replace({ name: "login" });
}
</script>

<template>
  <div class="shell-page">
    <header class="shell-header glass-panel">
      <div>
        <p class="shell-eyebrow">frps management</p>
        <h1 class="shell-title">管理面 WebUI</h1>
      </div>

      <nav class="shell-nav">
        <RouterLink
          class="shell-link"
          to="/"
        >
          概览
        </RouterLink>
        <RouterLink
          class="shell-link"
          to="/proxy-groups"
        >
          分组
        </RouterLink>
        <RouterLink
          class="shell-link"
          to="/tunnels"
        >
          隧道
        </RouterLink>
      </nav>

      <div class="shell-status">
        <el-tag :type="authStatusType">
          {{ authStatusLabel }}
        </el-tag>
        <span
          v-if="authenticated && expiresAt"
          class="shell-expiry"
        >
          到期 {{ formattedExpiry }}
        </span>
        <div class="shell-actions">
          <el-button
            plain
            :disabled="loggingOut"
            @click="handleRefresh"
          >
            刷新状态
          </el-button>
          <el-button
            type="danger"
            plain
            :loading="loggingOut"
            @click="handleLogout"
          >
            退出登录
          </el-button>
        </div>
      </div>
    </header>

    <main class="shell-main">
      <el-alert
        v-if="lastError"
        class="shell-alert"
        type="warning"
        :closable="false"
        show-icon
        :title="lastError"
      />
      <RouterView />
    </main>
  </div>
</template>
