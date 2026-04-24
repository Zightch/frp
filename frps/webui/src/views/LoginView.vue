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
  if (result.data) {
    if (!result.data.initialized) {
      router.replace({ name: 'Init' })
    } else if (result.data.authenticated) {
      router.replace({ name: 'GroupConfig' })
    }
  }
})

async function sha256Hex(input: string): Promise<string> {
  const data = new TextEncoder().encode(input)
  const hash = await crypto.subtle.digest('SHA-256', data)
  return Array.from(new Uint8Array(hash))
    .map(b => b.toString(16).padStart(2, '0'))
    .join('')
}

async function handleLogin() {
  if (!secret.value) {
    ElMessage.warning('请输入管理密钥')
    return
  }

  loading.value = true
  try {
    const challengeResult = await authApi.challenge()
    if (challengeResult.error) {
      ElMessage.error(challengeResult.error)
      return
    }

    const keyHash = await sha256Hex(secret.value)
    const proof = await sha256Hex(keyHash + challengeResult.data!.salt)

    const loginResult = await authApi.login(challengeResult.data!.challenge_id, proof)
    if (loginResult.error) {
      ElMessage.error(loginResult.error)
      return
    }

    ElMessage.success('登录成功')
    router.push({ name: 'GroupConfig' })
  } catch {
    ElMessage.error('登录失败')
  } finally {
    loading.value = false
  }
}
</script>

<template>
  <el-card v-loading="checking" element-loading-text="检查登录状态..." class="login-card">
    <template #header>
      <span class="card-title">管理登录</span>
    </template>
    <el-form @submit.prevent="handleLogin" class="login-form">
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
          @click="handleLogin"
        >
          登录
        </el-button>
      </el-form-item>
    </el-form>
  </el-card>
</template>

<style scoped>
.login-card {
  border-radius: var(--radius-base);
  border: 1px solid var(--color-border);
}

.card-title {
  font-size: 20px;
  font-weight: 600;
  color: var(--color-text-primary);
}

.login-form {
  display: flex;
  flex-direction: column;
  gap: var(--spacing-base);
}
</style>
