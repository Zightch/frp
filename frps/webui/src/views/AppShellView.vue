<script setup lang="ts">
import { computed } from "vue";
import { RouterLink } from "vue-router";
import { storeToRefs } from "pinia";

import { useAuthStore } from "@/stores/auth";

const authStore = useAuthStore();
const { authenticated, expiresAt, initialized, loading, ready } = storeToRefs(authStore);

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
          到期 {{ expiresAt }}
        </span>
      </div>
    </header>

    <main class="shell-main">
      <RouterView />
    </main>
  </div>
</template>
