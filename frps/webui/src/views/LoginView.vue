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
      router.replace('/init')
    } else if (result.data.authenticated) {
      router.replace('/proxy-groups')
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
    router.push('/proxy-groups')
  } catch {
    ElMessage.error('登录失败')
  } finally {
    loading.value = false
  }
}
</script>

<template>
  <div v-if="checking" class="auth-card">
    <p class="auth-checking">检查登录状态...</p>
  </div>
  <div v-else class="auth-card">
    <h2 class="auth-title">管理登录</h2>
    <el-form @submit.prevent="handleLogin">
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
          @click="handleLogin"
        >
          登录
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
  margin-bottom: var(--spacing-xl);
}
</style>