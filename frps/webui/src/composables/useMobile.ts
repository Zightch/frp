import { ref, onMounted, onUnmounted } from 'vue'

const MOBILE_BREAKPOINT = 768

/**
 * Composable for detecting mobile viewport.
 * Uses 768px as the breakpoint per Element Plus UI guidelines.
 */
export function useMobile() {
  const isMobile = ref(false)

  const checkMobile = () => {
    isMobile.value = window.innerWidth < MOBILE_BREAKPOINT
  }

  onMounted(() => {
    checkMobile()
    window.addEventListener('resize', checkMobile)
  })

  onUnmounted(() => {
    window.removeEventListener('resize', checkMobile)
  })

  return { isMobile }
}
