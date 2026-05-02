<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import { authApi } from '@/api'
import { sha256Hex } from '@/utils/sha256'

const router = useRouter()

const secret = ref('')
const loading = ref(false)
const checking = ref(true)

// Waiting state for admin occupied
const waitingState = ref(false)
const pendingLoginTicket = ref('')
const observedGeneration = ref(0)
const takeoverLoading = ref(false)

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
      // Check for 409 management_admin_occupied
      if (loginResult.errorCode === 'management_admin_occupied' && loginResult.details) {
        const details = loginResult.details as {
          pending_login_ticket: string
          observed_generation: number
        }
        pendingLoginTicket.value = details.pending_login_ticket
        observedGeneration.value = details.observed_generation
        waitingState.value = true
        return
      }
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

async function handleTakeover() {
  takeoverLoading.value = true
  try {
    const result = await authApi.takeover(pendingLoginTicket.value, observedGeneration.value)
    if (result.error) {
      // Check for 409 management_takeover_stale
      if (result.errorCode === 'management_takeover_stale') {
        ElMessage.error('页面已失效，请重新登录')
        resetToLoginForm()
        return
      }
      ElMessage.error(result.error)
      return
    }

    ElMessage.success('登录成功')
    router.push({ name: 'GroupConfig' })
  } catch {
    ElMessage.error('操作失败')
  } finally {
    takeoverLoading.value = false
  }
}

function resetToLoginForm() {
  waitingState.value = false
  pendingLoginTicket.value = ''
  observedGeneration.value = 0
}
</script>

<template>
  <el-card v-loading="checking" element-loading-text="检查登录状态..." class="login-card">
    <template #header>
      <span class="card-title">管理登录</span>
    </template>

    <!-- Login Form -->
    <el-form v-if="!waitingState" @submit.prevent="handleLogin" class="login-form">
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

    <el-result
      v-else
      icon="warning"
      title="当前已有管理员在线"
      sub-title="另一个管理员会话正在占用管理位，您可以选择顶掉并登录。"
    >
      <template #extra>
        <el-space wrap size="small">
          <el-button @click="resetToLoginForm">返回</el-button>
          <el-button type="primary" :loading="takeoverLoading" @click="handleTakeover">
            顶掉并登录
          </el-button>
        </el-space>
      </template>
    </el-result>
  </el-card>
</template>

<style scoped>
.login-card {
  border-radius: var(--radius-base);
  border: 1px solid var(--el-border-color);
}

.card-title {
  font-size: 20px;
  font-weight: 600;
  color: var(--el-text-color-primary);
}

.login-form {
  display: flex;
  flex-direction: column;
  gap: var(--spacing-base);
}

.full-width {
  width: 100%;
}
</style>
