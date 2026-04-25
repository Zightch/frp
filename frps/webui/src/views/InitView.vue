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
    router.replace({ name: 'Login' })
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
    router.push({ name: 'Login' })
  } catch {
    ElMessage.error('初始化失败')
  } finally {
    loading.value = false
  }
}
</script>

<template>
  <el-card v-loading="checking" element-loading-text="检查初始化状态..." class="init-card">
    <template #header>
      <span class="card-title">初始化管理密钥</span>
    </template>
    <p class="init-description">首次使用需设置管理密钥，此操作仅可执行一次</p>
    <el-form @submit.prevent="handleInit" class="init-form">
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
          class="full-width"
          @click="handleInit"
        >
          初始化
        </el-button>
      </el-form-item>
    </el-form>
  </el-card>
</template>

<style scoped>
.init-card {
  border-radius: var(--radius-base);
  border: 1px solid var(--el-border-color);
}

.card-title {
  font-size: 20px;
  font-weight: 600;
  color: var(--el-text-color-primary);
}

.init-description {
  color: var(--el-text-color-secondary);
  margin: 0 0 var(--spacing-base) 0;
  font-size: 14px;
  line-height: 22px;
}

.init-form {
  display: flex;
  flex-direction: column;
  gap: var(--spacing-base);
}
</style>
