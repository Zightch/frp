<script setup lang="ts">
import { ElMessage, type FormInstance, type FormRules } from "element-plus";
import { computed, reactive, ref } from "vue";
import { storeToRefs } from "pinia";
import { useRouter } from "vue-router";

import { useAuthStore } from "@/stores/auth";

type InitForm = {
  secret: string;
  confirmSecret: string;
};

const router = useRouter();
const authStore = useAuthStore();
const { authenticated, initializing, initialized, lastError, loading, ready } = storeToRefs(authStore);

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

const initializationLabel = computed(() => {
  if (!ready.value || loading.value) {
    return "状态读取中";
  }

  return initialized.value === false ? "等待初始化" : "已初始化";
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
    return "旧会话仍有效";
  }

  return "未建立管理会话";
});

const sessionType = computed(() => {
  if (!ready.value || loading.value) {
    return "info";
  }

  return authenticated.value ? "success" : "info";
});

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
    <div class="auth-stage">
      <div class="auth-hero glass-panel">
        <div class="auth-hero__copy">
          <p class="page-eyebrow">initialization</p>
          <h1 class="page-title">初始化管理密钥</h1>
          <p class="page-copy">
            当前服务端尚未发现本地 <code>auth.json</code>。首次初始化时，浏览器会先本地计算
            <code>sha256(secret)</code>，只把 <code>key_hash</code> 提交到
            <code>/api/v1/auth/init</code>；初始化完成后，服务端会一直沿用本地认证文件，直到删除
            <code>auth.json</code> 自动重置。
          </p>

          <div class="auth-status-grid">
            <div class="auth-status-card">
              <span class="auth-status-card__label">服务端状态</span>
              <strong class="auth-status-card__value">{{ initializationLabel }}</strong>
              <el-tag
                size="small"
                effect="plain"
                :type="initializationType"
              >
                {{ initialized === false ? "允许写入首个管理密钥" : "状态已变化" }}
              </el-tag>
            </div>

            <div class="auth-status-card">
              <span class="auth-status-card__label">管理会话</span>
              <strong class="auth-status-card__value">{{ sessionLabel }}</strong>
              <el-tag
                size="small"
                effect="plain"
                :type="sessionType"
              >
                {{ authenticated ? "会话状态已返回" : "当前未持有会话" }}
              </el-tag>
            </div>

            <div class="auth-status-card auth-status-card--wide">
              <span class="auth-status-card__label">提交边界</span>
              <strong class="auth-status-card__value">只提交管理密钥 hash，不上传明文</strong>
              <p class="auth-status-card__meta">
                当前阶段不提供在线轮换入口；如需整体重置认证状态，只能删除服务端本地 <code>auth.json</code>。
              </p>
            </div>
          </div>
        </div>

        <div class="auth-hero__side">
          <div class="auth-side-note">
            <span class="shell-side-note__label">当前约定</span>
            <p class="shell-side-note__copy">
              管理密钥只初始化一次，服务端只在本地 <code>auth.json</code> 中保存其 hash。
            </p>
            <p class="shell-side-note__copy">
              删除 <code>auth.json</code> 后，服务端会自动回到未初始化态，并清空旧会话。
            </p>
            <p class="shell-side-note__copy">
              初始化成功后页面会跳转到登录页，后续需要使用同一把管理密钥建立管理会话。
            </p>
          </div>
        </div>
      </div>

      <div class="auth-layout">
        <el-card class="auth-card auth-card--form glass-panel page-section-card">
          <template #header>
            <div class="management-card-header">
              <div>
                <div class="panel-header">写入首个管理密钥</div>
                <p class="management-card-copy">
                  这里只负责一次性初始化，不提供在线轮换。请确认你保存了明文管理密钥，再提交到服务端。
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
                type="error"
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
                浏览器只会上传 hash；初始化成功后，需要使用同一把管理密钥去登录并建立管理会话。
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
          </div>
        </el-card>

        <div class="auth-side-stack">
          <el-card class="auth-card auth-card--side glass-panel page-section-card">
            <template #header>
              <div class="management-card-header">
                <div>
                  <div class="panel-header">初始化流程</div>
                  <p class="management-card-copy">保持当前实现不变，只是把关键步骤直接呈现在页面上。</p>
                </div>
              </div>
            </template>

            <div class="auth-flow">
              <div class="auth-flow__item">
                <span class="auth-flow__eyebrow">step 01</span>
                <strong class="auth-flow__title">浏览器本地计算 <code>sha256(secret)</code></strong>
                <p class="auth-flow__copy">明文管理密钥只存在于当前输入过程，不会直接发送给服务端。</p>
              </div>

              <div class="auth-flow__item">
                <span class="auth-flow__eyebrow">step 02</span>
                <strong class="auth-flow__title">调用 <code>POST /api/v1/auth/init</code> 提交 <code>key_hash</code></strong>
                <p class="auth-flow__copy">服务端写入本地认证文件，并把当前管理认证状态返回给前端。</p>
              </div>

              <div class="auth-flow__item">
                <span class="auth-flow__eyebrow">step 03</span>
                <strong class="auth-flow__title">初始化完成后进入登录页</strong>
                <p class="auth-flow__copy">之后的认证入口只剩 challenge 登录，管理密钥本身不会在列表页回显。</p>
              </div>
            </div>
          </el-card>

          <el-card class="auth-card auth-card--side glass-panel page-section-card">
            <template #header>
              <div class="management-card-header">
                <div>
                  <div class="panel-header">重置与边界</div>
                  <p class="management-card-copy">这些规则会直接影响初始化页的实际使用方式。</p>
                </div>
              </div>
            </template>

            <ul class="auth-side-list">
              <li>只有服务端处于未初始化态时，路由守卫才允许进入本页。</li>
              <li>当前阶段不支持在线轮换管理密钥，也不提供第二把备用密钥。</li>
              <li>如果需要重置，只能删除服务端本地 <code>auth.json</code>，然后等待服务端自动回到未初始化态。</li>
              <li>删除 <code>auth.json</code> 会让旧会话失效，因此后续需要重新初始化并重新登录。</li>
            </ul>
          </el-card>
        </div>
      </div>
    </div>
  </section>
</template>
