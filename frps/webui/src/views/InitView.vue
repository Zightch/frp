<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import { authApi } from '@/api'

const router = useRouter()

const secret = ref('')
const loading = ref(false)
const checking = ref(true)

onMounted(async () => {
  const result = await authApi.state()
  checking.value = false
  if (result.data?.initialized) {
    router.replace('/login')
  }
})

async function sha256Hex(input: string): Promise<string> {
  const data = new TextEncoder().encode(input)
  const hash = await crypto.subtle.digest('SHA-256', data)
  return Array.from(new Uint8Array(hash))
    .map(b => b.toString(16).padStart(2, '0'))
    .join('')
}

async function handleInit() {
  if (!secret.value) {
    ElMessage.warning('请输入管理密钥')
    return
  }

  loading.value = true
  try {
    const keyHash = await sha256Hex(secret.value)
    const result = await authApi.init(keyHash)
    if (result.error) {
      ElMessage.error(result.error)
      return
    }
    ElMessage.success('管理密钥初始化成功')
    router.push('/login')
  } catch {
    ElMessage.error('初始化失败')
  } finally {
    loading.value = false
  }
}
</script>

<template>
  <div v-if="checking" class="auth-card">
    <p class="auth-checking">检查初始化状态...</p>
  </div>
  <div v-else class="auth-card">
    <h2 class="auth-title">初始化管理密钥</h2>
    <p class="auth-desc">首次使用需设置管理密钥，此操作仅可执行一次</p>
    <el-form @submit.prevent="handleInit">
      <el-form-item>
        <el-input
          v-model="secret"
          type="password"
          placeholder="管理密钥"
          show-password
          :disabled="loading"
        />
      </el-form-item>
      <el-form-item>
        <el-button
          type="primary"
          :loading="loading"
          style="width: 100%"
          @click="handleInit"
        >
          初始化
        </el-button>
      </el-form-item>
    </el-form>
  </div>
</template>

<style scoped>
.auth-card {
  padding: var(--spacing-2xl);
  background: var(--color-bg-white);
  border-radius: var(--radius-base);
  border: 1px solid var(--color-border-lighter);
}

.auth-checking {
  text-align: center;
  color: var(--color-text-secondary);
  padding: var(--spacing-lg) 0;
}

.auth-title {
  font-size: var(--font-size-h1);
  font-weight: var(--font-weight-bold);
  color: var(--color-text-primary);
  margin-bottom: var(--spacing-sm);
}

.auth-desc {
  font-size: var(--font-size-body);
  color: var(--color-text-secondary);
  margin-bottom: var(--spacing-xl);
}
</style>