import { defineStore } from "pinia";

import {
  fetchAuthState,
  fetchAuthSession,
  type AuthSessionResponse,
  type AuthStateResponse,
} from "@/api/auth";

function resolveMessage(error: unknown): string {
  if (typeof error === "object" && error !== null && "message" in error) {
    const message = (error as { message?: unknown }).message;
    if (typeof message === "string" && message.trim() !== "") {
      return message;
    }
  }
  return "管理认证状态读取失败";
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
    initialized: null as boolean | null,
    authenticated: false,
    expiresAt: null as string | null,
    lastError: "" as string,
  }),
  actions: {
    async bootstrap() {
      if (this.loading) {
        return;
      }

      this.loading = true;

      try {
        const authState = await fetchAuthState();
        applySnapshot(this, authState);

        if (authState.initialized && authState.authenticated) {
          try {
            const session = await fetchAuthSession();
            applySnapshot(this, session);
          } catch (error) {
            this.authenticated = false;
            this.expiresAt = null;
            this.lastError = resolveMessage(error);
          }
        } else {
          this.lastError = "";
        }
      } catch (error) {
        this.initialized = null;
        this.authenticated = false;
        this.expiresAt = null;
        this.lastError = resolveMessage(error);
      } finally {
        this.ready = true;
        this.loading = false;
      }
    },
    clearSession() {
      this.authenticated = false;
      this.expiresAt = null;
    },
  },
});
