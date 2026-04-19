import axios from "axios";

declare module "axios" {
  interface AxiosRequestConfig<D = any> {
    skipAuthRedirect?: boolean;
  }

  interface InternalAxiosRequestConfig<D = any> {
    skipAuthRedirect?: boolean;
  }
}

export class ApiError extends Error {
  readonly status?: number;

  constructor(message: string, status?: number) {
    super(message);
    this.name = "ApiError";
    this.status = status;
  }
}

type UnauthorizedHandler = (error: ApiError) => void | Promise<void>;

let unauthorizedHandler: UnauthorizedHandler | null = null;

export function setUnauthorizedHandler(handler: UnauthorizedHandler | null): void {
  unauthorizedHandler = handler;
}

export const http = axios.create({
  baseURL: "/api/v1",
  timeout: 10000,
  withCredentials: true,
});

http.interceptors.request.use((config) => {
  config.headers.set("Accept", "application/json");
  return config;
});

http.interceptors.response.use(
  (response) => response,
  (error: unknown) => {
    if (axios.isAxiosError(error)) {
      const payload = error.response?.data as { error?: unknown } | undefined;
      const message =
        typeof payload?.error === "string" && payload.error.trim() !== ""
          ? payload.error
          : error.message || "request failed";

      const apiError = new ApiError(message, error.response?.status);
      if (apiError.status === 401 && !error.config?.skipAuthRedirect && unauthorizedHandler) {
        void unauthorizedHandler(apiError);
      }

      return Promise.reject(apiError);
    }

    return Promise.reject(error);
  },
);
