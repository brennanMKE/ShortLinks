// Pure hash-routing logic (#0120): parsing `location.hash`, matching a parsed
// path against a route table, the Link.svelte "active" rule, and the
// login-redirect-target encoding used when an unauthenticated deep hash must
// bounce to /login without losing the intended destination. Nothing here
// touches the DOM or Svelte runes — see hashRouter.svelte.ts for the thin,
// stateful wiring around these functions, and routing.test.ts for coverage.

export interface ParsedHash {
  path: string;
  query: URLSearchParams;
}

/**
 * Parses `window.location.hash` into a normalized path and query.
 *
 * - Drops the leading `#`.
 * - An empty or missing hash defaults to `/`.
 * - Splits path from query on the first `?`; the query half is parsed with
 *   `URLSearchParams` — never `window.location.search`, which belongs to the
 *   outer document, not the SPA's own hash-routed state.
 * - Strips a trailing slash, except for the root `/` itself.
 */
export function parseHash(hash: string): ParsedHash {
  let raw = hash.startsWith('#') ? hash.slice(1) : hash;
  if (raw === '') raw = '/';

  const qIndex = raw.indexOf('?');
  let path = qIndex === -1 ? raw : raw.slice(0, qIndex);
  const queryString = qIndex === -1 ? '' : raw.slice(qIndex + 1);

  if (path === '') path = '/';
  if (path.length > 1 && path.endsWith('/')) path = path.slice(0, -1);

  return { path, query: new URLSearchParams(queryString) };
}

export interface RouteDef<T> {
  path: string;
  component: T;
}

export interface RouteMatch<T> {
  component: T;
  params: Record<string, string>;
}

function splitSegments(path: string): string[] {
  return path.split('/').filter((s) => s.length > 0);
}

/**
 * Matches `path` against `routes` in declaration order — first match wins.
 * `:param` matches exactly one path segment and lands in `params`, decoded
 * with `decodeURIComponent`. A route whose `path` is exactly `'*'` matches
 * anything; the caller's table should declare it last (the catch-all /
 * NotFound route) so nothing after it is ever reached, but this function
 * itself just returns on the first match regardless of position.
 */
export function matchRoute<T>(path: string, routes: RouteDef<T>[]): RouteMatch<T> | null {
  const segments = splitSegments(path);

  for (const route of routes) {
    if (route.path === '*') {
      return { component: route.component, params: {} };
    }

    const routeSegments = splitSegments(route.path);
    if (routeSegments.length !== segments.length) continue;

    const params: Record<string, string> = {};
    let matched = true;
    for (let i = 0; i < routeSegments.length; i++) {
      const rs = routeSegments[i];
      const s = segments[i];
      if (rs.startsWith(':')) {
        // A malformed percent-encoding (e.g. '100%', a truncated multi-byte
        // sequence) throws URIError — treat that as "this route doesn't
        // match" rather than letting it crash the router, so an unparseable
        // segment falls through to the catch-all NotFound route like any
        // other non-match.
        try {
          params[rs.slice(1)] = decodeURIComponent(s);
        } catch {
          matched = false;
          break;
        }
      } else if (rs !== s) {
        matched = false;
        break;
      }
    }
    if (matched) return { component: route.component, params };
  }

  return null;
}

/**
 * The `Link.svelte` "active" rule: exact match for the root path, prefix
 * match for everything else — so a link to `/campaigns` stays active while
 * viewing `/campaigns/spring-sale`.
 */
export function isActiveLink(currentPath: string, linkTo: string): boolean {
  if (linkTo === '/') return currentPath === '/';
  return currentPath === linkTo || currentPath.startsWith(linkTo + '/');
}

// ── Login redirect-target encoding ──────────────────────────────────────────
// An unauthenticated deep hash must bounce to the login route without losing
// the intended destination (#0120 AC: "Opening a deep hash while signed out
// goes to login, and signing in returns to that hash"). The destination path
// is carried as a `redirect` query parameter on the login route itself, e.g.
// '/login?redirect=%2Flinks%2Fabc123'.

export const REDIRECT_PARAM = 'redirect';

/** Builds the login path that preserves `intendedPath` as the redirect target. */
export function loginPathWithRedirect(intendedPath: string): string {
  if (intendedPath === '' || intendedPath === '/' || intendedPath === '/login') {
    return '/login';
  }
  return `/login?${REDIRECT_PARAM}=${encodeURIComponent(intendedPath)}`;
}

/**
 * Reads the redirect target back out of the login route's query, defaulting
 * to '/'. `URLSearchParams.get` already decodes its value, so no second
 * `decodeURIComponent` here (that double-decode previously mangled a target
 * whose original path itself contained a literal `%`). Rejects anything that
 * isn't a same-app path — including a protocol-relative `//host` or a
 * backslash variant `/\host` a browser can still resolve as one — since this
 * only ever feeds `replace()` and must never leave the app.
 */
export function redirectTargetFrom(query: URLSearchParams): string {
  const raw = query.get(REDIRECT_PARAM);
  if (!raw) return '/';
  if (!raw.startsWith('/') || raw.startsWith('//') || raw.startsWith('/\\')) return '/';
  return raw;
}
