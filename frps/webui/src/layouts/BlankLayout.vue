<script setup lang="ts">
import { RouterView } from 'vue-router'
</script>

<template>
  <el-container class="blank-layout">
    <el-main class="blank-main">
      <div class="center-wrapper">
        <RouterView />
      </div>
    </el-main>
  </el-container>
</template>

<style scoped>
.blank-layout {
  min-height: 100vh;
  background-color: var(--color-bg-page);
}

.blank-main {
  display: flex;
  justify-content: center;
  align-items: center;
  padding: var(--spacing-xl);
}

.center-wrapper {
  width: 100%;
  max-width: var(--login-card-max-width);
}
</style>
