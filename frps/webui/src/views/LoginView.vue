<script setup lang="ts">
import { ElMessage, type FormInstance, type FormRules } from "element-plus";
import { computed, reactive, ref } from "vue";
import { storeToRefs } from "pinia";
import { useRoute, useRouter } from "vue-router";

import { useAuthStore } from "@/stores/auth";
import { resolveRedirectTarget } from "@/utils/navigation";

type LoginForm = {
  secret: string;
};

const route = useRoute();
const router = useRouter();
const authStore = useAuthStore();
const { authenticated, expiresAt, initialized, lastError, loading, loggingIn, ready } = storeToRefs(authStore);

const formRef = ref<FormInstance>();
const form = reactive<LoginForm>({
  secret: "",
});

const redirectTarget = computed(() => resolveRedirectTarget(route.query.redirect) ?? "/");
const redirectLabel = computed(() => (redirectTarget.value === "/" ? "管理概览" : redirectTarget.value));

const rules: FormRules<LoginForm> = {
  secret: [
    {
      required: true,
      message: "请输入管理密钥",
      trigger: "blur",
    },
  ],
};

const initializationLabel = computed(() => {
  if (!ready.value || loading.value) {
    return "状态读取中";
  }

  if (initialized.value === false) {
    return "尚未初始化";
  }

  return "允许登录";
});

const initializationType = computed(() => {
  if (!ready.value || loading.value) {
    return "info";
  }

  return initialized.value === false ? "warning" : "success";
});

const sessionLabel = computed(() => {
  if (!ready.value || loading.value) {
    return "状态读取中";
  }
  if (authenticated.value) {
    return "管理会话有效";
  }
  if (initialized.value === false) {
    return "等待初始化";
  }

  return "等待输入管理密钥";
});

const sessionType = computed(() => {
  if (!ready.value || loading.value) {
    return "info";
  }

  return authenticated.value ? "success" : "info";
});

const expiryLabel = computed(() => {
  if (!authenticated.value) {
    return "登录成功后建立新的管理会话";
  }

  return formatDateTime(expiresAt.value) || "未返回到期时间";
});

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

async function handleSubmit(): Promise<void> {
  if (!formRef.value) {
    return;
  }

  try {
    await formRef.value.validate();
    await authStore.login(form.secret);
    form.secret = "";
    ElMessage.success("管理会话已建立。");
    await router.replace(redirectTarget.value);
  } catch {
    return;
  }
}

async function handleRefresh(): Promise<void> {
  await authStore.bootstrap(true);
}
</script>

<template>
  <section class="auth-page">
    <div class="auth-stage">
      <div class="auth-hero glass-panel">
        <div class="auth-hero__copy">
          <p class="page-eyebrow">challenge login</p>
          <h1 class="page-title">管理密钥登录</h1>
          <p class="page-copy">
            浏览器会先请求一次性 challenge，再本地计算
            <code>key_hash = sha256(secret)</code> 和
            <code>proof = sha256(key_hash + salt)</code>，最终只把 proof 发给服务端验证，并通过
            HttpOnly Cookie 建立管理会话。
          </p>

          <div class="auth-status-grid">
            <div class="auth-status-card">
              <span class="auth-status-card__label">认证前提</span>
              <strong class="auth-status-card__value">{{ initializationLabel }}</strong>
              <el-tag
                size="small"
                effect="plain"
                :type="initializationType"
              >
                {{ initialized === false ? "需先进入初始化页" : "可以发起 challenge 登录" }}
              </el-tag>
            </div>

            <div class="auth-status-card">
              <span class="auth-status-card__label">当前会话</span>
              <strong class="auth-status-card__value">{{ sessionLabel }}</strong>
              <el-tag
                size="small"
                effect="plain"
                :type="sessionType"
              >
                {{ authenticated ? "已返回有效会话" : "尚未建立会话" }}
              </el-tag>
            </div>

            <div class="auth-status-card auth-status-card--wide">
              <span class="auth-status-card__label">登录后去向</span>
              <strong class="auth-status-card__value">{{ redirectLabel }}</strong>
              <p class="auth-status-card__meta">如果携带了受保护页面的跳转参数，登录成功后会优先返回该页面。</p>
            </div>
          </div>
        </div>

        <div class="auth-hero__side">
          <div class="auth-side-note">
            <span class="shell-side-note__label">当前边界</span>
            <p class="shell-side-note__copy">
              登录阶段只接受当前管理密钥，不提供在线轮换，也不会返回明文密钥本身。
            </p>
            <p class="shell-side-note__copy">
              服务端删除 <code>auth.json</code> 后会回到未初始化态，旧管理会话随之失效。
            </p>
            <p class="shell-side-note__copy">
              会话 token 通过 HttpOnly Cookie 保存，浏览器脚本无法直接读取其明文。
            </p>
          </div>
        </div>
      </div>

      <div class="auth-layout">
        <el-card class="auth-card auth-card--form glass-panel page-section-card">
          <template #header>
            <div class="management-card-header">
              <div>
                <div class="panel-header">输入管理密钥并建立会话</div>
                <p class="management-card-copy">
                  登录成功后会返回到目标管理页；如果当前会话过期，需要重新走一轮 challenge 登录。
                </p>
              </div>
            </div>
          </template>

          <div class="auth-card__stack">
            <div
              v-if="lastError"
              class="page-feedback"
            >
              <el-alert
                type="warning"
                :closable="false"
                show-icon
                :title="lastError"
              />
            </div>

            <el-form
              ref="formRef"
              class="auth-form"
              :model="form"
              :rules="rules"
              label-position="top"
              status-icon
              @submit.prevent="handleSubmit"
            >
              <el-form-item
                label="管理密钥"
                prop="secret"
              >
                <el-input
                  v-model="form.secret"
                  type="password"
                  show-password
                  autocomplete="current-password"
                  placeholder="输入管理密钥"
                  @keyup.enter="handleSubmit"
                />
              </el-form-item>

              <p class="auth-form-note">
                会话到期信息：{{ expiryLabel }}。如果你是从受保护页面跳转过来，登录成功后会返回 {{ redirectLabel }}。
              </p>

              <div class="auth-form-actions">
                <el-button
                  type="primary"
                  :loading="loggingIn"
                  :disabled="loading"
                  @click="handleSubmit"
                >
                  登录管理面
                </el-button>
                <el-button
                  plain
                  :disabled="loggingIn"
                  @click="handleRefresh"
                >
                  刷新状态
                </el-button>
              </div>
            </el-form>
          </div>
        </el-card>

        <div class="auth-side-stack">
          <el-card class="auth-card auth-card--side glass-panel page-section-card">
            <template #header>
              <div class="management-card-header">
                <div>
                  <div class="panel-header">登录流程</div>
                  <p class="management-card-copy">challenge-proof 语义保持原样，只重做信息层次和可读性。</p>
                </div>
              </div>
            </template>

            <div class="auth-flow">
              <div class="auth-flow__item">
                <span class="auth-flow__eyebrow">step 01</span>
                <strong class="auth-flow__title">请求一次性 challenge</strong>
                <p class="auth-flow__copy">前端调用 <code>POST /api/v1/auth/challenge</code> 获取挑战 ID 和盐值。</p>
              </div>

              <div class="auth-flow__item">
                <span class="auth-flow__eyebrow">step 02</span>
                <strong class="auth-flow__title">浏览器本地计算 <code>proof</code></strong>
                <p class="auth-flow__copy">
                  使用 <code>sha256(secret)</code> 和 challenge salt 计算 proof，明文管理密钥不会直接发送到服务端。
                </p>
              </div>

              <div class="auth-flow__item">
                <span class="auth-flow__eyebrow">step 03</span>
                <strong class="auth-flow__title">建立管理会话并跳转</strong>
                <p class="auth-flow__copy">服务端校验成功后返回管理会话，前端按 redirect 参数回到目标页或首页。</p>
              </div>
            </div>
          </el-card>

          <el-card class="auth-card auth-card--side glass-panel page-section-card">
            <template #header>
              <div class="management-card-header">
                <div>
                  <div class="panel-header">会话与重置边界</div>
                  <p class="management-card-copy">这些说明只覆盖当前已经实现的认证与会话模型。</p>
                </div>
              </div>
            </template>

            <ul class="auth-side-list">
              <li>登录成功后依赖 HttpOnly Cookie 保持管理会话，浏览器脚本不直接读取会话 token。</li>
              <li>如果 challenge 已失效、已被使用或 proof 校验失败，需要重新发起登录。</li>
              <li>服务端删除 <code>auth.json</code> 后会自动回到未初始化态，此时本页会重新让位给初始化页。</li>
              <li>当前管理面只覆盖概览、分组管理和隧道管理，不在登录页扩展未来能力入口。</li>
            </ul>
          </el-card>
        </div>
      </div>
    </div>
  </section>
</template>
