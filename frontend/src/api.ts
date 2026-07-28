import type {
  AdminTrafficResponse,
  LoginResponse,
  NodeTotalsResponse,
  RemoteServersResponse,
  Session,
  TrafficSummary,
  UserConnectionsResponse,
  UserSpeedsResponse,
  UsersTrafficResponse,
} from "./types";

const SESSION_KEY = "mmwx-session";

export function loadSession(): Session | null {
  const raw = localStorage.getItem(SESSION_KEY);
  if (!raw) return null;
  try {
    const session = JSON.parse(raw) as Session;
    if (!session.token || new Date(session.expiresAt).getTime() <= Date.now()) {
      localStorage.removeItem(SESSION_KEY);
      return null;
    }
    return session;
  } catch {
    localStorage.removeItem(SESSION_KEY);
    return null;
  }
}

export function saveSession(response: LoginResponse): Session {
  const session: Session = {
    token: response.token,
    username: response.username,
    nickname: response.nickname,
    avatarUrl: response.avatar_url,
    role: response.role,
    isAdmin: response.is_admin,
    expiresAt: response.expires_at,
  };
  localStorage.setItem(SESSION_KEY, JSON.stringify(session));
  return session;
}

export function clearSession() {
  localStorage.removeItem(SESSION_KEY);
}

async function request<T>(path: string, token?: string, init?: RequestInit): Promise<T> {
  const headers = new Headers(init?.headers);
  if (token) headers.set("MM-Authorization", token);
  if (init?.body && !headers.has("Content-Type")) {
    headers.set("Content-Type", "application/json");
  }
  const response = await fetch(path, { ...init, headers });
  if (!response.ok) {
    let message = `请求失败 (${response.status})`;
    try {
      const body = (await response.json()) as { error?: string; message?: string };
      message = body.error || body.message || message;
    } catch {
      // Keep the status-based message.
    }
    throw new Error(message);
  }
  return (await response.json()) as T;
}

export async function login(username: string, password: string, rememberMe: boolean) {
  return request<LoginResponse>("/api/login", undefined, {
    method: "POST",
    body: JSON.stringify({
      username,
      password,
      remember_me: rememberMe,
      turnstile_token: "",
    }),
  });
}

export function fetchTrafficSummary(token: string) {
  return request<TrafficSummary>("/api/traffic/summary", token);
}

export function fetchRemoteServers(token: string) {
  return request<RemoteServersResponse>("/api/admin/remote-servers", token);
}

export function fetchNodeTotals(token: string, date: string) {
  return request<NodeTotalsResponse>(`/api/admin/traffic/node-totals?date=${encodeURIComponent(date)}`, token);
}

export function fetchUsers(token: string) {
  return request<UsersTrafficResponse>("/api/admin/traffic/users", token);
}

export function fetchUserConnections(token: string) {
  return request<UserConnectionsResponse>("/api/admin/traffic/user-connections", token);
}

export function fetchUserSpeeds(token: string, serverId: number) {
  return request<UserSpeedsResponse>(`/api/admin/remote/user-speeds?server_id=${encodeURIComponent(String(serverId))}`, token);
}

export function fetchAdminTraffic(token: string) {
  return request<AdminTrafficResponse>("/api/admin/traffic/servers", token);
}

export function controlRemoteService(token: string, serverId: number, service: "xray", action: "start" | "stop" | "restart") {
  return request<{ success?: boolean; message?: string }>(`/api/admin/remote/services/control?server_id=${encodeURIComponent(String(serverId))}`, token, {
    method: "POST",
    body: JSON.stringify({ service, action }),
  });
}
