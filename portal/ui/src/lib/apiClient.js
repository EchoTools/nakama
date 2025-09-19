// Simple API client with auth + automatic refresh on 401 for RPC/REST calls
// Assumes the following env vars:
// - VITE_NAKAMA_API_BASE (e.g. http://localhost:7350/v2)
// - VITE_NAKAMA_SERVER_KEY for refresh endpoint basic auth

const API_BASE = import.meta.env.VITE_NAKAMA_API_BASE;
const NAKAMA_SERVER_KEY = import.meta.env.VITE_NAKAMA_SERVER_KEY;

function getToken() {
  return localStorage.getItem('jwt');
}
function getRefreshToken() {
  return localStorage.getItem('refreshToken');
}
function setToken(token) {
  if (token) localStorage.setItem('jwt', token);
}

function defaultHeaders() {
  const headers = {};
  headers['Content-Type'] = 'application/json';
  const jwt = getToken();
  if (jwt) headers['Authorization'] = `Bearer ${jwt}`;
  return headers;
}

async function refreshSession() {
  const rt = getRefreshToken();
  if (!rt) return null;
  const res = await fetch(`${API_BASE}/account/session/refresh?unwrap`, {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      Authorization: `Basic ${btoa(`${NAKAMA_SERVER_KEY}:`)}`,
    },
    body: JSON.stringify({ refresh_token: rt }),
  });
  const data = await res.json().catch(() => ({}));
  if (res.ok && data.token) {
    setToken(data.token);
    return data.token;
  }
  return null;
}

export async function apiFetch(path, opts = {}, { autoRefresh = true } = {}) {
  let url = path.startsWith('http') ? path : `${API_BASE}${path.startsWith('/') ? '' : '/'}${path}`;

  // Add unwrap query param if not already present
  const urlObj = new URL(url);
  if (!urlObj.searchParams.has('unwrap')) {
    urlObj.searchParams.set('unwrap', '');
    url = urlObj.toString();
  }

  const auth = defaultHeaders();

  const init = { ...opts, headers: { ...(opts.headers || {}), ...auth } };
  console.log(init);
  let res = await fetch(url, init);
  if (res.status === 401 && autoRefresh) {
    const newToken = await refreshSession();
    console.log(newToken);
    if (newToken) {
      const retryInit = {
        ...init,
        headers: { ...init.headers, Authorization: `Bearer ${newToken}` },
      };
      res = await fetch(url, retryInit);
    }
  }
  return res;
}

// Convenience RPC helpers
export async function callRpc(id, body, opts = {}) {
  return apiFetch(
    `/rpc/${id}`,
    {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body ?? {}),
    },
    opts
  );
}

export async function get(path, opts = {}) {
  return apiFetch(path, { method: 'GET', ...(opts || {}) }, opts);
}

export async function post(path, body, opts = {}) {
  return apiFetch(
    path,
    { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) },
    opts
  );
}
