const encoder = new TextEncoder();

function bytesToHex(bytes: Uint8Array): string {
  return Array.from(bytes, (value) => value.toString(16).padStart(2, "0")).join("");
}

export async function sha256Hex(value: string): Promise<string> {
  const digest = await crypto.subtle.digest("SHA-256", encoder.encode(value));
  return bytesToHex(new Uint8Array(digest));
}

export async function buildManagementKeyHash(secret: string): Promise<string> {
  return sha256Hex(secret);
}

export async function buildChallengeProof(keyHash: string, salt: string): Promise<string> {
  return sha256Hex(keyHash + salt);
}
