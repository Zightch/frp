<script setup lang="ts">
import { RouterView } from 'vue-router'
</script>

<template>
  <div style="min-height: 100vh; display: flex; justify-content: center; align-items: center; background: var(--el-bg-color-page)">
    <div style="width: 100%; max-width: 400px">
      <RouterView />
    </div>
  </div>
</template>
