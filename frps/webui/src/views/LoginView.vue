<template>
  <section class="page-stack">
    <el-card class="glass-panel narrow-panel">
      <p class="page-eyebrow">下一步功能</p>
      <h2 class="page-title">管理密钥登录</h2>
      <p class="page-copy">
        真实登录页会先请求一次性 challenge，再由浏览器本地计算
        <code>sha256(key_hash + salt)</code> 并提交 proof，服务端验证通过后再签发管理会话。
      </p>

      <el-timeline>
        <el-timeline-item timestamp="1">
          调用 <code>POST /api/v1/auth/challenge</code> 获取一次性盐。
        </el-timeline-item>
        <el-timeline-item timestamp="2">
          浏览器本地计算 proof，不回传明文密钥。
        </el-timeline-item>
        <el-timeline-item timestamp="3">
          调用 <code>POST /api/v1/auth/login</code> 完成会话建立。
        </el-timeline-item>
      </el-timeline>
    </el-card>
  </section>
</template>
