declare global {
  interface Window {
    __FRPS_WEBUI_BASE_PATH__?: string
  }
}

function normalizeBasePath(value: string): string {
  const trimmed = value.trim()
  if (!trimmed || trimmed === '/') {
    return ''
  }

  let normalized = trimmed.startsWith('/') ? trimmed : `/${trimmed}`
  normalized = normalized.replace(/\/{2,}/g, '/')

  if (normalized.length > 1 && normalized.endsWith('/')) {
    normalized = normalized.slice(0, -1)
  }

  return normalized
}

function readRuntimeBasePath(): string {
  if (typeof window !== 'undefined' && typeof window.__FRPS_WEBUI_BASE_PATH__ === 'string') {
    return window.__FRPS_WEBUI_BASE_PATH__
  }

  if (typeof document !== 'undefined') {
    const baseElement = document.querySelector('base')
    if (baseElement?.href) {
      return new URL(baseElement.href).pathname
    }
  }

  return ''
}

export const WEBUI_BASE_PATH = normalizeBasePath(readRuntimeBasePath())
export const WEBUI_HISTORY_BASE = WEBUI_BASE_PATH ? `${WEBUI_BASE_PATH}/` : '/'
export const API_BASE = WEBUI_BASE_PATH ? `${WEBUI_BASE_PATH}/api/v1` : '/api/v1'

export {}
