import { createRouter, createWebHistory } from "vue-router";

declare module "vue-router" {
  interface RouteMeta {
    title?: string;
    public?: boolean;
    requiresSession?: boolean;
  }
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
            title: "管理概览",
          },
        },
        {
          path: "proxy-groups",
          name: "proxy-groups",
          component: () => import("@/views/ProxyGroupsView.vue"),
          meta: {
            title: "分组管理",
          },
        },
        {
          path: "tunnels",
          name: "tunnels",
          component: () => import("@/views/TunnelsView.vue"),
          meta: {
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

router.afterEach((to) => {
  const pageTitle = to.meta.title ? `${to.meta.title} | frps Management` : "frps Management";
  document.title = pageTitle;
});
