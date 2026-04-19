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
        <p class="page-eyebrow">Phase 2 已落地</p>
        <h2 class="page-title">Vue 3 + Vite + Element Plus 基础工程已接线</h2>
        <p class="page-copy">
          当前阶段先完成工程骨架、依赖接入、路由、Pinia 和 Axios 基础封装。下一步进入真正的管理密钥初始化页与 challenge 登录页。
        </p>

        <div class="hero-actions">
          <RouterLink to="/init">
            <el-button type="primary">
              查看初始化页占位
            </el-button>
          </RouterLink>
          <RouterLink to="/login">
            <el-button plain>
              查看登录页占位
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
          <li>当前页面仍以占位为主，真实表单和守卫在下一阶段补齐。</li>
        </ul>
      </el-card>

      <el-card class="glass-panel">
        <template #header>
          <div class="panel-header">
            <span>下一步工作</span>
          </div>
        </template>

        <ul class="panel-list">
          <li>实现 InitSecret 页面和密钥 hash 初始化提交。</li>
          <li>实现 Login 页面与浏览器本地 proof 计算。</li>
          <li>补齐路由守卫、Axios 会话失效处理和会话恢复。</li>
        </ul>
      </el-card>
    </div>
  </section>
</template>
