<script setup lang="ts">
import { storeToRefs } from "pinia";
import { RouterLink } from "vue-router";

import { useAuthStore } from "@/stores/auth";

const authStore = useAuthStore();
const { authenticated, initialized, lastError, ready } = storeToRefs(authStore);
</script>

<template>
  <section class="page-stack">
    <div class="hero-grid">
      <el-card class="hero-card hero-card--feature">
        <p class="page-eyebrow">Phase 3 已落地</p>
        <h2 class="page-title">管理密钥初始化与 challenge 登录闭环已接通</h2>
        <p class="page-copy">
          当前 WebUI 已具备初始化管理密钥、恢复管理会话、登录、登出、路由守卫和未授权自动回跳能力。下一步进入分组与隧道管理页面迁移。
        </p>

        <div class="hero-actions">
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
      </el-card>

      <div class="metric-grid">
        <el-card class="metric-card">
          <span class="metric-label">管理密钥状态</span>
          <strong class="metric-value">
            {{
              initialized === null
                ? "未读取"
                : initialized
                  ? "已初始化"
                  : "未初始化"
            }}
          </strong>
        </el-card>

        <el-card class="metric-card">
          <span class="metric-label">管理会话状态</span>
          <strong class="metric-value">
            {{ authenticated ? "已登录" : "未登录" }}
          </strong>
        </el-card>

        <el-card class="metric-card">
          <span class="metric-label">前端加载状态</span>
          <strong class="metric-value">
            {{ ready ? "已就绪" : "加载中" }}
          </strong>
        </el-card>
      </div>
    </div>

    <el-alert
      v-if="lastError"
      type="warning"
      :closable="false"
      show-icon
      :title="lastError"
    />

    <div class="panel-grid">
      <el-card class="glass-panel">
        <template #header>
          <div class="panel-header">
            <span>当前工程边界</span>
          </div>
        </template>

        <ul class="panel-list">
          <li>不再引入任何 admin/admins 账号体系。</li>
          <li>管理认证只依赖本地 auth.json 中保存的管理密钥 hash。</li>
          <li>浏览器登录固定走一次性盐 challenge proof。</li>
          <li>管理会话当前只保存在内存中，服务端重启后需要重新登录。</li>
        </ul>
      </el-card>

      <el-card class="glass-panel">
        <template #header>
          <div class="panel-header">
            <span>下一步工作</span>
          </div>
        </template>

        <ul class="panel-list">
          <li>迁移 proxy-groups 列表、创建、编辑、删除和 token 重置。</li>
          <li>迁移 tunnels 列表、创建、编辑和删除。</li>
          <li>最终把构建产物嵌入到 frps 服务端。</li>
        </ul>
      </el-card>
    </div>
  </section>
</template>
