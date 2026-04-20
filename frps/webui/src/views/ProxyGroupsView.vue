<script setup lang="ts">
import { ref, onMounted, watch } from 'vue'
import { useRouter } from 'vue-router'
import { authApi, proxyGroupsApi, tunnelsApi } from '@/api'

const router = useRouter()

const checking = ref(true)
const initialized = ref(false)
const authenticated = ref(false)

const groups = ref<Array<{ id: string; name: string; token_id: string }>>([])
const tunnels = ref<Array<{ id: string; name: string; group_id: string; type: string; local_addr: string; remote_addr: string }>>([])
const selectedGroupId = ref<string | null>(null)
const loading = ref(false)
const error = ref('')

onMounted(async () => {
  const authResult = await authApi.state()
  checking.value = false

  if (!authResult.data) {
    error.value = '无法检查认证状态'
    return
  }

  if (!authResult.data.initialized) {
    router.replace('/init')
    return
  }

  initialized.value = true

  if (!authResult.data.authenticated) {
    router.replace('/login')
    return
  }

  authenticated.value = true
  await loadData()
})

async function loadData() {
  loading.value = true
  error.value = ''

  try {
    const [groupsResult, tunnelsResult] = await Promise.all([
      proxyGroupsApi.list(),
      tunnelsApi.list()
    ])

    if (groupsResult.error) {
      error.value = groupsResult.error
      return
    }

    if (tunnelsResult.error) {
      error.value = tunnelsResult.error
      return
    }

    groups.value = groupsResult.data?.groups || []
    tunnels.value = tunnelsResult.data?.tunnels || []

    if (groups.value.length > 0 && !selectedGroupId.value) {
      selectedGroupId.value = groups.value[0].id
    }
  } catch {
    error.value = '加载数据失败'
  } finally {
    loading.value = false
  }
}

const filteredTunnels = ref<typeof tunnels.value>([])

watch([selectedGroupId, tunnels], ([groupId, allTunnels]) => {
  if (groupId) {
    filteredTunnels.value = allTunnels.filter(t => t.group_id === groupId)
  } else {
    filteredTunnels.value = []
  }
}, { immediate: true })

function getGroupName(groupId: string): string {
  const group = groups.value.find(g => g.id === groupId)
  return group?.name || groupId
}

function handleGroupRowClick(row: { id: string; name: string; token_id: string }) {
  selectedGroupId.value = row.id
}

function getGroupRowClass({ row }: { row: { id: string; name: string; token_id: string } }): string {
  return row.id === selectedGroupId.value ? 'selected-row' : ''
}
</script>

<template>
  <!-- Auth check loading -->
  <div v-if="checking" class="state-message">
    检查认证状态...
  </div>

  <!-- Error state -->
  <div v-else-if="error" class="state-message state-error">
    <span>{{ error }}</span>
    <el-button size="small" @click="loadData">重试</el-button>
  </div>

  <!-- Main content -->
  <template v-else-if="authenticated">
    <!-- Page header -->
    <div class="page-header">
      <h1>接入管理</h1>
      <el-button @click="loadData" :loading="loading">刷新</el-button>
    </div>

    <!-- Loading -->
    <div v-if="loading" class="state-message">
      加载中...
    </div>

    <!-- Content -->
    <div v-else class="content-grid">
      <!-- Groups -->
      <section class="content-section">
        <h2>分组列表</h2>
        <div v-if="groups.length === 0" class="empty-message">
          暂无分组
        </div>
        <el-table v-else :data="groups" @row-click="handleGroupRowClick" :row-class-name="getGroupRowClass">
          <el-table-column prop="name" label="名称" />
          <el-table-column prop="token_id" label="Token ID" width="280" />
        </el-table>
      </section>

      <!-- Tunnels -->
      <section class="content-section">
        <h2>
          隧道列表
          <span v-if="selectedGroupId" class="section-subtitle">（{{ getGroupName(selectedGroupId) }}）</span>
        </h2>
        <div v-if="!selectedGroupId" class="empty-message">
          请选择分组查看隧道
        </div>
        <div v-else-if="filteredTunnels.length === 0" class="empty-message">
          该分组暂无隧道
        </div>
        <el-table v-else :data="filteredTunnels">
          <el-table-column prop="name" label="名称" />
          <el-table-column prop="type" label="类型" width="80" />
          <el-table-column prop="local_addr" label="本地地址" />
          <el-table-column prop="remote_addr" label="远端地址" />
        </el-table>
      </section>
    </div>
  </template>
</template>

<style scoped>
.page-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: var(--spacing-xl);
}

.page-header h1 {
  font-size: var(--font-size-h1);
  font-weight: var(--font-weight-bold);
  color: var(--color-text-primary);
  margin: 0;
}

.state-message {
  text-align: center;
  padding: var(--spacing-2xl);
  color: var(--color-text-secondary);
}

.state-error {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: var(--spacing-md);
  color: var(--color-danger);
}

.content-grid {
  display: flex;
  flex-direction: column;
  gap: var(--spacing-xl);
}

.content-section {
  background: var(--color-bg-white);
  border-radius: var(--radius-base);
  border: 1px solid var(--color-border-lighter);
  padding: var(--spacing-lg);
}

.content-section h2 {
  font-size: var(--font-size-h2);
  font-weight: var(--font-weight-bold);
  color: var(--color-text-primary);
  margin: 0 0 var(--spacing-base) 0;
}

.section-subtitle {
  font-weight: var(--font-weight-regular);
  font-size: var(--font-size-body);
  color: var(--color-text-secondary);
}

.empty-message {
  text-align: center;
  padding: var(--spacing-2xl) 0;
  color: var(--color-text-secondary);
}

:deep(.selected-row) {
  background-color: var(--color-primary-light-9);
}

:deep(.el-table) {
  cursor: pointer;
}
</style>