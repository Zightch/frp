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
    // 1. Get challenge
    const challengeResult = await authApi.challenge()
    if (challengeResult.error) {
      ElMessage.error(challengeResult.error)
      return
    }

    // 2. Compute proof: proof = SHA256(key_hash + salt)
    const keyHash = await sha256Hex(secret.value)
    const proof = await sha256Hex(keyHash + challengeResult.data!.salt)

    // 3. Login
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
  <div class="login-page">
    <div v-if="checking" class="login-card">
      <p>检查登录状态...</p>
    </div>
    <div v-else class="login-card">
      <h2>管理登录</h2>
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
  </div>
</template>

<style scoped>
.login-page {
  display: flex;
  justify-content: center;
  align-items: center;
  min-height: 100vh;
}

.login-card {
  width: 360px;
  padding: 32px;
  background: #fff;
  border-radius: 8px;
  border: 1px solid var(--border-color);
}

.login-card h2 {
  margin-bottom: 24px;
}
</style>
