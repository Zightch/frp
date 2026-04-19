import axios from "axios";

export class ApiError extends Error {
  readonly status?: number;

  constructor(message: string, status?: number) {
    super(message);
    this.name = "ApiError";
    this.status = status;
  }
}

export const http = axios.create({
  baseURL: "/api/v1",
  timeout: 10000,
  withCredentials: true,
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

      return Promise.reject(new ApiError(message, error.response?.status));
    }

    return Promise.reject(error);
  },
);
