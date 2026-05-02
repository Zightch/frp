<script setup lang="ts">
import { nextTick, ref, watch } from 'vue'
import type { FormInstance, FormRules } from 'element-plus'
import type {
  RatePolicy,
  RatePolicyMode,
  RatePolicyPayload,
  RatePolicyUnit
} from '@/api'

defineOptions({
  name: 'RatePolicyFormDialog'
})

const props = defineProps<{
  modelValue: boolean
  mode: 'create' | 'edit'
  policy?: RatePolicy | null
  submitting: boolean
}>()

const emit = defineEmits<{
  (event: 'update:modelValue', value: boolean): void
  (event: 'submit', payload: RatePolicyPayload): void
}>()

const formRef = ref<FormInstance>()
const form = ref<{
  name: string
  mode: RatePolicyMode
  downlink_value: number | null
  downlink_unit: RatePolicyUnit
  uplink_value: number | null
  uplink_unit: RatePolicyUnit
}>({
  name: '',
  mode: 'independent',
  downlink_value: null,
  downlink_unit: 'M',
  uplink_value: null,
  uplink_unit: 'M'
})

const rules: FormRules<typeof form.value> = {
  name: [{ required: true, message: '请输入策略名称', trigger: 'blur' }],
  mode: [{ required: true, message: '请选择模式', trigger: 'change' }],
  downlink_value: [{ required: true, message: '请输入下行速率', trigger: 'blur' }],
  uplink_value: [{ required: true, message: '请输入上行速率', trigger: 'blur' }]
}

watch(
  () => [props.modelValue, props.mode, props.policy?.id] as const,
  async ([visible]) => {
    if (!visible) {
      return
    }

    if (props.mode === 'edit' && props.policy) {
      form.value = {
        name: props.policy.name,
        mode: props.policy.mode,
        downlink_value: props.policy.downlink_value,
        downlink_unit: props.policy.downlink_unit,
        uplink_value: props.policy.uplink_value,
        uplink_unit: props.policy.uplink_unit
      }
    } else {
      form.value = {
        name: '',
        mode: 'independent',
        downlink_value: null,
        downlink_unit: 'M',
        uplink_value: null,
        uplink_unit: 'M'
      }
    }

    await nextTick()
    formRef.value?.clearValidate()
  },
  { immediate: true }
)

async function handleSubmit() {
  const valid = await formRef.value?.validate().catch(() => false)
  if (!valid) {
    return
  }

  emit('submit', {
    name: form.value.name.trim(),
    mode: form.value.mode,
    downlink_value: form.value.downlink_value as number,
    downlink_unit: form.value.downlink_unit,
    uplink_value: form.value.uplink_value as number,
    uplink_unit: form.value.uplink_unit
  })
}
</script>

<template>
  <el-dialog
    :model-value="modelValue"
    :title="mode === 'create' ? '新建策略' : '编辑策略'"
    width="400px"
    :close-on-click-modal="false"
    @update:model-value="emit('update:modelValue', $event)"
  >
    <el-form
      ref="formRef"
      :model="form"
      :rules="rules"
      label-width="80px"
      class="policy-form"
    >
      <el-form-item label="名称" prop="name">
        <el-input v-model="form.name" placeholder="请输入策略名称" />
      </el-form-item>
      <el-form-item label="模式" prop="mode">
        <el-select
          v-model="form.mode"
          class="full-width"
          :disabled="mode === 'edit'"
        >
          <el-option label="独享" value="independent" />
          <el-option label="共享" value="shared" />
        </el-select>
      </el-form-item>

      <el-divider content-position="left">下行速率</el-divider>
      <el-form-item prop="downlink_value">
        <el-row :gutter="8">
          <el-col :span="16">
            <el-input-number
              v-model="form.downlink_value"
              :min="1"
              :max="form.downlink_unit === 'K' ? 999999 : form.downlink_unit === 'M' ? 999 : 99"
              :controls="false"
              class="full-width"
              placeholder="速率"
            />
          </el-col>
          <el-col :span="8">
            <el-select v-model="form.downlink_unit" class="full-width">
              <el-option label="Kbps" value="K" />
              <el-option label="Mbps" value="M" />
              <el-option label="Gbps" value="G" />
            </el-select>
          </el-col>
        </el-row>
      </el-form-item>

      <el-divider content-position="left">上行速率</el-divider>
      <el-form-item prop="uplink_value">
        <el-row :gutter="8">
          <el-col :span="16">
            <el-input-number
              v-model="form.uplink_value"
              :min="1"
              :max="form.uplink_unit === 'K' ? 999999 : form.uplink_unit === 'M' ? 999 : 99"
              :controls="false"
              class="full-width"
              placeholder="速率"
            />
          </el-col>
          <el-col :span="8">
            <el-select v-model="form.uplink_unit" class="full-width">
              <el-option label="Kbps" value="K" />
              <el-option label="Mbps" value="M" />
              <el-option label="Gbps" value="G" />
            </el-select>
          </el-col>
        </el-row>
      </el-form-item>
    </el-form>

    <template #footer>
      <el-button @click="emit('update:modelValue', false)">取消</el-button>
      <el-button type="primary" :loading="submitting" @click="handleSubmit">
        {{ mode === 'create' ? '创建' : '保存' }}
      </el-button>
    </template>
  </el-dialog>
</template>

<style scoped>
.policy-form :deep(.el-form-item__label) {
  white-space: nowrap;
}

.full-width {
  width: 100%;
}
</style>
