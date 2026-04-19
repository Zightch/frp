<script setup lang="ts">
import { ElMessage, type FormInstance, type FormRules } from "element-plus";
import { reactive, ref } from "vue";
import { storeToRefs } from "pinia";
import { useRouter } from "vue-router";

import { useAuthStore } from "@/stores/auth";

type InitForm = {
  secret: string;
  confirmSecret: string;
};

const router = useRouter();
const authStore = useAuthStore();
const { initializing, lastError, loading } = storeToRefs(authStore);

const formRef = ref<FormInstance>();
const form = reactive<InitForm>({
  secret: "",
  confirmSecret: "",
});

const rules: FormRules<InitForm> = {
  secret: [
    {
      required: true,
      message: "请输入管理密钥",
      trigger: "blur",
    },
  ],
  confirmSecret: [
    {
      required: true,
      message: "请再次输入管理密钥",
      trigger: "blur",
    },
    {
      validator: (_rule, value: string, callback) => {
        if (value === "") {
          callback(new Error("请再次输入管理密钥"));
          return;
        }
        if (value !== form.secret) {
          callback(new Error("两次输入的管理密钥不一致"));
          return;
        }
        callback();
      },
      trigger: ["blur", "change"],
    },
  ],
};

async function handleSubmit(): Promise<void> {
  if (!formRef.value) {
    return;
  }

  try {
    await formRef.value.validate();
    await authStore.initializeSecret(form.secret);
    form.secret = "";
    form.confirmSecret = "";
    ElMessage.success("管理密钥已初始化，请使用该密钥登录。");
    await router.replace({ name: "login" });
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
        <p class="page-eyebrow">Initialization</p>
        <h1 class="page-title">初始化管理密钥</h1>
        <p class="page-copy">
          当前服务端尚未发现本地 <code>auth.json</code>。浏览器会先本地计算
          <code>sha256(secret)</code>，只把 <code>key_hash</code> 提交到
          <code>/api/v1/auth/init</code>，不会上传明文管理密钥。
        </p>

        <div class="auth-detail-list">
          <div class="auth-detail">
            <strong>初始化条件</strong>
            <span>只有未初始化状态允许访问本页。</span>
          </div>
          <div class="auth-detail">
            <strong>存储方式</strong>
            <span>服务端仅在本地 <code>auth.json</code> 中保存管理密钥 hash。</span>
          </div>
          <div class="auth-detail">
            <strong>注意事项</strong>
            <span>初始化完成后请妥善保存该管理密钥；如需重置，需删除服务端本地 <code>auth.json</code> 后重新初始化。</span>
          </div>
        </div>
      </el-card>

      <el-card class="auth-card auth-card--form glass-panel">
        <el-alert
          v-if="lastError"
          class="auth-alert"
          type="error"
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
              autocomplete="new-password"
              placeholder="输入新的管理密钥"
            />
          </el-form-item>

          <el-form-item
            label="确认管理密钥"
            prop="confirmSecret"
          >
            <el-input
              v-model="form.confirmSecret"
              type="password"
              show-password
              autocomplete="new-password"
              placeholder="再次输入管理密钥"
              @keyup.enter="handleSubmit"
            />
          </el-form-item>

          <p class="auth-form-note">
            浏览器只会上传 hash，明文管理密钥不会发给服务端。
          </p>

          <div class="auth-form-actions">
            <el-button
              type="primary"
              :loading="initializing"
              :disabled="loading"
              @click="handleSubmit"
            >
              初始化管理密钥
            </el-button>
            <el-button
              plain
              :disabled="initializing"
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
