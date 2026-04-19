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
const { lastError, loading, loggingIn } = storeToRefs(authStore);

const formRef = ref<FormInstance>();
const form = reactive<LoginForm>({
  secret: "",
});

const redirectTarget = computed(() => resolveRedirectTarget(route.query.redirect) ?? "/");

const rules: FormRules<LoginForm> = {
  secret: [
    {
      required: true,
      message: "请输入管理密钥",
      trigger: "blur",
    },
  ],
};

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
    <div class="auth-layout">
      <el-card class="auth-card auth-card--accent glass-panel">
        <p class="page-eyebrow">Challenge Login</p>
        <h1 class="page-title">管理密钥登录</h1>
        <p class="page-copy">
          浏览器先请求一次性 challenge，再本地计算
          <code>key_hash = sha256(secret)</code> 和
          <code>proof = sha256(key_hash + salt)</code>，最终只把 proof 发给服务端验证。
        </p>

        <div class="auth-detail-list">
          <div class="auth-detail">
            <strong>第一步</strong>
            <span>调用 <code>POST /api/v1/auth/challenge</code> 获取一次性盐。</span>
          </div>
          <div class="auth-detail">
            <strong>第二步</strong>
            <span>浏览器本地计算 challenge proof，不上传明文管理密钥。</span>
          </div>
          <div class="auth-detail">
            <strong>第三步</strong>
            <span>调用 <code>POST /api/v1/auth/login</code> 建立管理会话。</span>
          </div>
        </div>
      </el-card>

      <el-card class="auth-card auth-card--form glass-panel">
        <el-alert
          v-if="lastError"
          class="auth-alert"
          type="warning"
          :closable="false"
          show-icon
          :title="lastError"
        />

        <el-form
          ref="formRef"
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
            登录成功后会使用 HttpOnly Cookie 建立管理会话，浏览器脚本无法直接读取会话 token。
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
      </el-card>
    </div>
  </section>
</template>
