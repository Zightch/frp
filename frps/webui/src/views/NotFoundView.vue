<template>
  <section class="page-stack">
    <el-card class="glass-panel narrow-panel">
      <el-result
        icon="warning"
        title="页面不存在"
        sub-title="当前路由未定义，请返回管理首页。"
      >
        <template #extra>
          <RouterLink to="/">
            <el-button type="primary">
              返回首页
            </el-button>
          </RouterLink>
        </template>
      </el-result>
    </el-card>
  </section>
</template>
