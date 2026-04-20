<script setup lang="ts">
import { RouterView } from 'vue-router'
</script>

<template>
  <div class="blank-layout">
    <div class="blank-center">
      <RouterView />
    </div>
  </div>
</template>

<style scoped>
.blank-layout {
  min-height: 100vh;
  background: var(--color-bg-page);
  display: flex;
  justify-content: center;
  align-items: center;
}

.blank-center {
  width: 100%;
  max-width: 400px;
}
</style>