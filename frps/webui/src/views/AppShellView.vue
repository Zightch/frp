<script setup lang="ts">
import { computed, ref, watch } from "vue";
import { storeToRefs } from "pinia";
import { ElMessage } from "element-plus";
import { useRoute, useRouter, type RouteRecordNormalized } from "vue-router";

import { useAuthStore } from "@/stores/auth";

type ManagementNavItem = {
  name: string;
  path: string;
  title: string;
  navLabel: string;
  eyebrow: string;
  description: string;
  order: number;
};

const router = useRouter();
const route = useRoute();
const authStore = useAuthStore();
const mobileNavOpen = ref(false);
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

const sessionExpiryLabel = computed(() => {
  if (!authenticated.value) {
    return "未登录";
  }

  return formattedExpiry.value || "未返回到期时间";
});

function isManagementRoute(record: RouteRecordNormalized): record is RouteRecordNormalized & {
  name: string;
  meta: RouteRecordNormalized["meta"] & {
    title: string;
    navLabel: string;
    eyebrow: string;
    description: string;
    navOrder?: number;
    managementNav: true;
  };
} {
  return (
    record.meta.managementNav === true &&
    typeof record.name === "string" &&
    typeof record.meta.title === "string" &&
    typeof record.meta.navLabel === "string" &&
    typeof record.meta.eyebrow === "string" &&
    typeof record.meta.description === "string"
  );
}

const managementNavItems = computed<ManagementNavItem[]>(() =>
  router
    .getRoutes()
    .filter(isManagementRoute)
    .map((record) => ({
      name: record.name,
      path: record.path,
      title: record.meta.title,
      navLabel: record.meta.navLabel,
      eyebrow: record.meta.eyebrow,
      description: record.meta.description,
      order: record.meta.navOrder ?? Number.MAX_SAFE_INTEGER,
    }))
    .sort((left, right) => left.order - right.order),
);

const currentManagementItem = computed<ManagementNavItem | undefined>(() => {
  const currentName = route.name?.toString();
  if (!currentName) {
    return undefined;
  }

  return managementNavItems.value.find((item) => item.name === currentName);
});

const pageEyebrow = computed(() => currentManagementItem.value?.eyebrow ?? "management");
const pageTitle = computed(() => currentManagementItem.value?.title ?? "管理面板");
const pageDescription = computed(
  () => currentManagementItem.value?.description ?? "查看当前系统状态与管理入口。",
);

watch(
  () => route.fullPath,
  () => {
    mobileNavOpen.value = false;
  },
);

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
  <div class="app-shell">
    <button
      class="shell-sidebar-backdrop"
      :class="{ 'is-open': mobileNavOpen }"
      type="button"
      aria-label="关闭导航"
      @click="mobileNavOpen = false"
    />

    <aside
      class="shell-sidebar glass-panel"
      :class="{ 'is-open': mobileNavOpen }"
    >
      <div class="shell-sidebar__brand">
        <p class="shell-sidebar__eyebrow">frps control</p>
        <h1 class="shell-sidebar__title">管理面板</h1>
        <p class="shell-sidebar__copy">
          当前管理面只承接概览、分组管理和隧道管理，不额外包装未来功能入口。
        </p>
      </div>

      <nav
        class="shell-sidebar__nav"
        aria-label="管理导航"
      >
        <RouterLink
          v-for="item in managementNavItems"
          :key="item.name"
          class="shell-nav-link"
          :class="{ 'is-active': item.name === currentManagementItem?.name }"
          :to="{ name: item.name }"
        >
          <span class="shell-nav-link__eyebrow">{{ item.eyebrow }}</span>
          <strong class="shell-nav-link__title">{{ item.navLabel }}</strong>
          <span class="shell-nav-link__copy">{{ item.description }}</span>
        </RouterLink>
      </nav>

      <div class="shell-side-note">
        <span class="shell-side-note__label">当前边界</span>
        <p class="shell-side-note__copy">管理密钥只初始化一次并持久化在服务端本地 `auth.json`。</p>
        <p class="shell-side-note__copy">删除 `auth.json` 后，服务端会自动回到未初始化态并清空旧会话。</p>
      </div>
    </aside>

    <div class="shell-stage">
      <header class="shell-topbar glass-panel">
        <div class="shell-topbar__identity">
          <el-button
            class="shell-mobile-toggle"
            text
            @click="mobileNavOpen = true"
          >
            菜单
          </el-button>

          <p class="shell-topbar__eyebrow">{{ pageEyebrow }}</p>
          <h2 class="shell-topbar__title">{{ pageTitle }}</h2>
          <p class="shell-topbar__description">{{ pageDescription }}</p>
        </div>

        <div class="shell-topbar__session">
          <div class="shell-status-grid">
            <div class="shell-status-card">
              <span class="shell-status-card__label">认证状态</span>
              <el-tag :type="authStatusType">
                {{ authStatusLabel }}
              </el-tag>
            </div>

            <div class="shell-status-card">
              <span class="shell-status-card__label">会话到期</span>
              <strong class="shell-status-card__value">{{ sessionExpiryLabel }}</strong>
            </div>
          </div>

          <div class="shell-topbar__actions">
            <el-button
              plain
              :disabled="loggingOut"
              :loading="loading"
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

      <section
        v-if="lastError"
        class="shell-alert-slot"
      >
        <el-alert
          class="shell-alert"
          type="warning"
          :closable="false"
          show-icon
          :title="lastError"
        />
      </section>

      <main class="shell-content">
        <div class="page-shell">
          <RouterView />
        </div>
      </main>
    </div>
  </div>
</template>
