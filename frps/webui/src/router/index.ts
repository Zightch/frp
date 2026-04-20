import { createRouter, createWebHistory } from "vue-router";

import { pinia } from "@/stores";
import { useAuthStore } from "@/stores/auth";
import { resolveRedirectTarget } from "@/utils/navigation";

declare module "vue-router" {
  interface RouteMeta {
    description?: string;
    eyebrow?: string;
    managementNav?: boolean;
    navLabel?: string;
    navOrder?: number;
    title?: string;
    public?: boolean;
    requiresSession?: boolean;
  }
}

function buildLoginQuery(path: string): { redirect: string } | undefined {
  if (path === "/login") {
    return undefined;
  }

  return { redirect: path };
}

export const router = createRouter({
  history: createWebHistory(),
  routes: [
    {
      path: "/init",
      name: "init",
      component: () => import("@/views/InitSecretView.vue"),
      meta: {
        title: "初始化管理密钥",
        public: true,
      },
    },
    {
      path: "/login",
      name: "login",
      component: () => import("@/views/LoginView.vue"),
      meta: {
        title: "管理密钥登录",
        public: true,
      },
    },
    {
      path: "/",
      component: () => import("@/views/AppShellView.vue"),
      meta: {
        title: "管理概览",
        requiresSession: true,
      },
      children: [
        {
          path: "",
          name: "dashboard",
          component: () => import("@/views/DashboardView.vue"),
          meta: {
            description: "查看当前管理认证状态、系统边界和两类核心管理入口。",
            eyebrow: "overview",
            managementNav: true,
            navLabel: "概览",
            navOrder: 1,
            title: "管理概览",
          },
        },
        {
          path: "proxy-groups",
          name: "proxy-groups",
          component: () => import("@/views/ProxyGroupsView.vue"),
          meta: {
            description: "管理接入分组、启停状态和对应 token 生命周期。",
            eyebrow: "proxy groups",
            managementNav: true,
            navLabel: "分组管理",
            navOrder: 2,
            title: "分组管理",
          },
        },
        {
          path: "tunnels",
          name: "tunnels",
          component: () => import("@/views/TunnelsView.vue"),
          meta: {
            description: "管理 TCP/UDP 单端口映射与端口范围映射配置。",
            eyebrow: "tunnels",
            managementNav: true,
            navLabel: "隧道管理",
            navOrder: 3,
            title: "隧道管理",
          },
        },
      ],
    },
    {
      path: "/:pathMatch(.*)*",
      name: "not-found",
      component: () => import("@/views/NotFoundView.vue"),
      meta: {
        title: "页面不存在",
        public: true,
      },
    },
  ],
});

router.beforeEach(async (to) => {
  const authStore = useAuthStore(pinia);
  if (!authStore.ready || authStore.loading) {
    await authStore.bootstrap();
  }

  if (to.name === "not-found") {
    return true;
  }

  if (authStore.initialized === false) {
    if (to.name !== "init") {
      return { name: "init" };
    }
    return true;
  }

  if (authStore.initialized === true && authStore.authenticated) {
    if (to.name === "login" || to.name === "init") {
      return resolveRedirectTarget(to.query.redirect) ?? { name: "dashboard" };
    }
    return true;
  }

  if (to.meta.requiresSession) {
    return {
      name: "login",
      query: buildLoginQuery(to.fullPath),
    };
  }

  if (authStore.initialized === true && to.name === "init") {
    return { name: "login" };
  }

  return true;
});

router.afterEach((to) => {
  const pageTitle = to.meta.title ? `${to.meta.title} | frps Management` : "frps Management";
  document.title = pageTitle;
});
