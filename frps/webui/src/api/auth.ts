import { http } from "@/api/http";

type AuthResponseShape = {
  initialized: boolean;
  authenticated: boolean;
  expires_at?: string;
};

export type AuthStateResponse = {
  initialized: boolean;
  authenticated: boolean;
  expiresAt?: string;
};

export type AuthSessionResponse = AuthStateResponse;

export type InitSecretPayload = {
  keyHash: string;
};

export type AuthChallengeResponse = {
  challengeId: string;
  salt: string;
  expiresAt: string;
};

export type LoginPayload = {
  challengeId: string;
  proof: string;
};

function normalizeAuthResponse(payload: AuthResponseShape): AuthStateResponse {
  return {
    initialized: payload.initialized,
    authenticated: payload.authenticated,
    expiresAt: payload.expires_at,
  };
}

export async function fetchAuthState(): Promise<AuthStateResponse> {
  const response = await http.get<AuthResponseShape>("/auth/state");
  return normalizeAuthResponse(response.data);
}

export async function initializeManagementSecret(payload: InitSecretPayload): Promise<AuthStateResponse> {
  const response = await http.post<AuthResponseShape>("/auth/init", {
    key_hash: payload.keyHash,
  });
  return normalizeAuthResponse(response.data);
}

export async function requestLoginChallenge(): Promise<AuthChallengeResponse> {
  const response = await http.post<{
    challenge_id: string;
    salt: string;
    expires_at: string;
  }>("/auth/challenge");

  return {
    challengeId: response.data.challenge_id,
    salt: response.data.salt,
    expiresAt: response.data.expires_at,
  };
}

export async function loginWithProof(payload: LoginPayload): Promise<AuthSessionResponse> {
  const response = await http.post<AuthResponseShape>("/auth/login", {
    challenge_id: payload.challengeId,
    proof: payload.proof,
  });
  return normalizeAuthResponse(response.data);
}

export async function fetchAuthSession(): Promise<AuthSessionResponse> {
  const response = await http.get<AuthResponseShape>("/auth/session");
  return normalizeAuthResponse(response.data);
}

export async function logoutManagementSession(): Promise<void> {
  await http.post("/auth/logout");
}
