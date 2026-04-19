<template>
  <section class="page-stack">
    <el-card class="glass-panel narrow-panel">
      <p class="page-eyebrow">下一步功能</p>
      <h2 class="page-title">初始化管理密钥</h2>
      <p class="page-copy">
        这一页将在下一阶段接入真实表单。浏览器会先本地计算
        <code>sha256(secret)</code>，只把 <code>key_hash</code> 发给服务端的
        <code>/api/v1/auth/init</code>，不会上传明文管理密钥。
      </p>

      <el-descriptions
        :column="1"
        border
      >
        <el-descriptions-item label="前置条件">
          仅在 <code>auth.json</code> 不存在、服务端返回未初始化时允许访问。
        </el-descriptions-item>
        <el-descriptions-item label="提交目标">
          <code>POST /api/v1/auth/init</code>
        </el-descriptions-item>
        <el-descriptions-item label="当前状态">
          本页骨架已创建，真实表单和校验待下一步完成。
        </el-descriptions-item>
      </el-descriptions>
    </el-card>
  </section>
</template>
