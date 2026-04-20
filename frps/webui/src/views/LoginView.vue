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
</script>

<template>
  <section class="auth-page auth-page--minimal">
    <div class="auth-login-panel glass-panel">
      <div class="auth-login-header">
        <p class="page-eyebrow">challenge login</p>
        <h1 class="page-title auth-login-title">管理密钥登录</h1>
      </div>

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
        class="auth-login-form"
        :model="form"
        :rules="rules"
        hide-required-asterisk
        @submit.prevent="handleSubmit"
      >
        <el-form-item prop="secret">
          <el-input
            v-model="form.secret"
            type="password"
            size="large"
            show-password
            autocomplete="current-password"
            placeholder="输入管理密钥"
            @input="authStore.clearError()"
            @keyup.enter="handleSubmit"
          />
        </el-form-item>

        <div class="auth-login-actions">
          <el-button
            type="primary"
            size="large"
            :loading="loggingIn"
            :disabled="loading"
            @click="handleSubmit"
          >
            登录
          </el-button>
        </div>
      </el-form>
    </div>
  </section>
</template>
