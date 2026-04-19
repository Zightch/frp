import { defineStore } from "pinia";

import {
  fetchAuthSession,
  fetchAuthState,
  initializeManagementSecret,
  loginWithProof,
  logoutManagementSession,
  requestLoginChallenge,
  type AuthSessionResponse,
  type AuthStateResponse,
} from "@/api/auth";
import { ApiError } from "@/api/http";
import { buildChallengeProof, buildManagementKeyHash } from "@/utils/crypto";

const knownErrorMessages: Record<string, string> = {
  "challenge has already been used": "登录挑战已被使用，请重新尝试。",
  "challenge is missing or expired": "登录挑战已过期，请重新尝试。",
  "challenge proof is invalid": "管理密钥错误，或登录挑战已失效。",
  "management auth is unavailable": "管理认证服务当前不可用。",
  "management key hash must be 64 lowercase hex characters": "管理密钥哈希格式无效。",
  "management secret is already initialized": "管理密钥已经初始化。",
  "management secret is not initialized": "管理密钥尚未初始化。",
  "management session is invalid or expired": "管理会话已失效，请重新登录。",
  "management session is required": "需要先登录管理面。",
};

let bootstrapPromise: Promise<void> | null = null;

function translateMessage(message: string): string {
  const normalized = message.trim();
  if (normalized === "") {
    return "请求失败";
  }

  if (knownErrorMessages[normalized]) {
    return knownErrorMessages[normalized];
  }
  if (normalized.includes("Network Error")) {
    return "无法连接管理 API，请确认 frps 服务正在运行。";
  }
  if (normalized.includes("timeout")) {
    return "请求超时，请确认 frps 服务已经启动且响应正常。";
  }

  return normalized;
}

function resolveMessage(error: unknown, fallback = "管理认证状态读取失败"): string {
  if (error instanceof ApiError) {
    return translateMessage(error.message);
  }

  if (typeof error === "object" && error !== null && "message" in error) {
    const message = (error as { message?: unknown }).message;
    if (typeof message === "string" && message.trim() !== "") {
      return translateMessage(message);
    }
  }

  return fallback;
}

function applySnapshot(
  target: {
    initialized: boolean | null;
    authenticated: boolean;
    expiresAt: string | null;
  },
  snapshot: AuthStateResponse | AuthSessionResponse,
): void {
  target.initialized = snapshot.initialized;
  target.authenticated = snapshot.authenticated;
  target.expiresAt = snapshot.expiresAt ?? null;
}

export const useAuthStore = defineStore("auth", {
  state: () => ({
    ready: false,
    loading: false,
    initializing: false,
    loggingIn: false,
    loggingOut: false,
    initialized: null as boolean | null,
    authenticated: false,
    expiresAt: null as string | null,
    lastError: "" as string,
  }),
  getters: {
    busy: (state) => state.loading || state.initializing || state.loggingIn || state.loggingOut,
  },
  actions: {
    async bootstrap(force = false) {
      if (this.loading && bootstrapPromise) {
        return bootstrapPromise;
      }
      if (this.ready && !force) {
        return;
      }

      bootstrapPromise = (async () => {
        this.loading = true;

        try {
          const authState = await fetchAuthState();
          applySnapshot(this, authState);
          this.lastError = "";

          if (!authState.initialized || !authState.authenticated) {
            if (!authState.authenticated) {
              this.expiresAt = null;
            }
            return;
          }

          try {
            const session = await fetchAuthSession();
            applySnapshot(this, session);
          } catch (error) {
            this.handleUnauthorized(resolveMessage(error, "管理会话已失效，请重新登录。"));
          }
        } catch (error) {
          this.initialized = null;
          this.authenticated = false;
          this.expiresAt = null;
          this.lastError = resolveMessage(error);
        } finally {
          this.ready = true;
          this.loading = false;
          bootstrapPromise = null;
        }
      })();

      return bootstrapPromise;
    },
    async initializeSecret(secret: string) {
      this.initializing = true;
      this.lastError = "";

      try {
        const keyHash = await buildManagementKeyHash(secret);
        const snapshot = await initializeManagementSecret({ keyHash });
        applySnapshot(this, snapshot);
        this.ready = true;
        return snapshot;
      } catch (error) {
        this.lastError = resolveMessage(error, "管理密钥初始化失败");
        throw error;
      } finally {
        this.initializing = false;
      }
    },
    async login(secret: string) {
      this.loggingIn = true;
      this.lastError = "";

      try {
        const keyHash = await buildManagementKeyHash(secret);
        const challenge = await requestLoginChallenge();
        const proof = await buildChallengeProof(keyHash, challenge.salt);
        const session = await loginWithProof({
          challengeId: challenge.challengeId,
          proof,
        });
        applySnapshot(this, session);
        this.ready = true;
        return session;
      } catch (error) {
        this.authenticated = false;
        this.expiresAt = null;
        this.lastError = resolveMessage(error, "管理密钥登录失败");
        throw error;
      } finally {
        this.loggingIn = false;
      }
    },
    async logout() {
      this.loggingOut = true;

      try {
        await logoutManagementSession();
        this.lastError = "";
      } catch (error) {
        this.lastError = resolveMessage(error, "管理会话退出失败");
      } finally {
        this.clearSession();
        this.loggingOut = false;
      }
    },
    clearSession() {
      this.authenticated = false;
      this.expiresAt = null;
    },
    clearError() {
      this.lastError = "";
    },
    handleUnauthorized(message = "管理会话已失效，请重新登录。") {
      this.clearSession();
      if (this.initialized !== false) {
        this.initialized = true;
      }
      this.lastError = message;
      this.ready = true;
    },
  },
});
