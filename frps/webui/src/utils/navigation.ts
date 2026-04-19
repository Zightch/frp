export function resolveRedirectTarget(value: unknown): string | null {
  if (typeof value !== "string") {
    return null;
  }

  const target = value.trim();
  if (target === "" || !target.startsWith("/") || target.startsWith("//")) {
    return null;
  }
  if (target.startsWith("/login") || target.startsWith("/init")) {
    return null;
  }

  return target;
}
