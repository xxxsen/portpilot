export class ApiError extends Error {
  constructor(
    public status: number,
    message: string,
  ) {
    super(message);
  }
}

type ApiOptions = RequestInit & {csrf?: string};

export async function api<T = void>(path: string, options: ApiOptions = {}): Promise<T> {
  const headers = new Headers(options.headers);
  if (options.body) headers.set("Content-Type", "application/json");
  if (options.csrf) headers.set("X-CSRF-Token", options.csrf);
  const response = await fetch(path, {
    ...options,
    headers,
    credentials: "include",
    cache: "no-store",
  });
  if (!response.ok) {
    const payload = await response.json().catch(() => ({})) as {error?: string; detail?: string};
    throw new ApiError(response.status, payload.detail ?? payload.error ?? `HTTP ${response.status}`);
  }
  if (response.status === 204) return undefined as T;
  return response.json() as Promise<T>;
}
