export const API = '/mcp/app/api/';
let csrf = '';
export function setCSRF(value: string) { csrf = value; }
export class ApiError extends Error { constructor(message: string, public status: number) { super(message); } }
export async function api<T>(path: string, options: RequestInit = {}): Promise<T> {
  const response = await fetch(API + path, {
    credentials: 'same-origin', cache: 'no-store', ...options,
    headers: { 'Content-Type': 'application/json', 'X-MCPX-Console': '1', ...(csrf ? { 'X-MCPX-CSRF': csrf } : {}), ...options.headers },
  });
  const data = await response.json().catch(() => ({}));
  if (!response.ok) { if(response.status===401)window.dispatchEvent(new Event('mcpx:unauthorized'));throw new ApiError(data.error || `请求失败 (${response.status})`, response.status); }
  return data as T;
}
export function query(workspace: string, session = ''): string { return new URLSearchParams({ workspace, session_id: session }).toString(); }
export const message = (error: unknown) => error instanceof Error ? error.message : '请求失败，请重试';
