// Svelte 5 runes hash router (#0120). Holds the current parsed+matched route
// as `$state` so every consumer (Router.svelte, Link.svelte, App.svelte)
// reacts to `hashchange` without a svelte/store subscription. The pure
// parsing/matching logic lives in routing.ts, unit-tested with no Svelte
// runtime at all; this module is the thin, imperative, DOM-touching wiring
// around it — see routing.test.ts for the actual coverage.
//
// The route TABLE (the array of { path, component }) is deliberately not
// declared here: it needs the real view components, which only App.svelte
// imports. initRouter() takes the table as a parameter instead, so this
// module never imports a single .svelte view and stays trivially testable in
// isolation (even though, in practice, its own logic is exercised through
// routing.ts's pure functions rather than directly).

import { parseHash, matchRoute, type RouteDef } from './routing';

export interface RouteState {
  path: string;
  params: Record<string, string>;
  query: URLSearchParams;
  component: unknown;
}

let routes: RouteDef<unknown>[] = [];
let hashChangeWired = false;

function compute(): RouteState {
  const { path, query } = parseHash(window.location.hash);
  const match = matchRoute(path, routes);
  return {
    path,
    params: match?.params ?? {},
    query,
    component: match?.component ?? null,
  };
}

let route = $state<RouteState>({
  path: '/',
  params: {},
  query: new URLSearchParams(),
  component: null,
});

/**
 * Wires the route table into the router and computes the initial route from
 * the current `location.hash`. Call once, from App.svelte's onMount, before
 * the app's first render — the route table lives there because it needs the
 * imported view components (#0120 Notes). Safe to call more than once (e.g.
 * a dev-mode HMR remount): the table is replaced but the `hashchange`
 * listener is only ever registered a single time.
 */
export function initRouter(routeTable: RouteDef<unknown>[]): void {
  routes = routeTable;
  route = compute();
  if (!hashChangeWired) {
    window.addEventListener('hashchange', () => {
      route = compute();
    });
    hashChangeWired = true;
  }
}

/**
 * Reactive accessor for the current route. Call this from inside a
 * component's template, a `$derived`, or an `$effect` — reading the module's
 * `$state` variable through a function call like this (rather than exporting
 * it directly) is what lets reactivity survive the module boundary.
 */
export function getRoute(): RouteState {
  return route;
}

/**
 * Navigates to `path`, pushing a new history entry — the normal way to move
 * between screens. Setting `location.hash` is itself what a real `<a
 * href="#...">` click does (see Link.svelte), so this exists for the
 * programmatic call sites (e.g. a table row's click handler) that aren't a
 * literal anchor.
 */
export function navigate(path: string): void {
  window.location.hash = path;
  // `hashchange` fires asynchronously; recompute synchronously too so a
  // caller reading getRoute() immediately after navigate() (rather than
  // waiting for the next hashchange) sees the new route right away.
  route = compute();
}

/**
 * Replaces the current hash with `path`, adding no history entry. Use this
 * for redirects and normalization: bouncing an unauthenticated user to
 * /login, or normalizing an empty/malformed hash on first load. Guard every
 * call on a component's init path against the CURRENT route (e.g. "only
 * replace to /login if we are not already there") — replace() itself does
 * not loop-detect.
 */
export function replace(path: string): void {
  window.location.replace('#' + path);
  route = compute();
}
