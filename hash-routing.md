# ShortLinks SPA: Hash-Based Routing

Task spec for Claude Code. Add client-side hash routing to the Svelte 5 admin
SPA so screens are addressable, shareable, and work with browser back/forward.

## Goal

Every screen in the admin SPA gets a URL of the form
`https://go.sstools.co/admin/#/campaigns/42`. Loading that URL directly lands on
that screen. Back and forward move between screens. No server route changes are
required.

## Why hash and not pushState

The SPA is served from a `go:embed` bundle behind Apache. `pushState` paths
would require a catch-all rule so that `/admin/campaigns/42` returns
`index.html` instead of 404. Hash routing avoids that entirely: the fragment is
never sent to the server, so the existing single static handler keeps working.

Accepted tradeoffs: URLs carry a `#`, and the route is invisible to server logs
and server-side analytics. Both are fine for an authenticated admin UI.

## Hard constraint: do not touch the redirect path

`/u/{key}` is handled by the Go service and must remain untouched. This work is
scoped to the admin SPA only. Do not add any client-side interception of `/u/`
URLs, and do not change the Go router except where explicitly listed in
"Server-side changes" below.

## Deliverables

1. `src/lib/router.svelte.ts` - the router store and route matching.
2. `src/lib/Link.svelte` - anchor component that produces correct `href` values.
3. `src/lib/Router.svelte` - renders the component for the current route.
4. Route table wired into `App.svelte`.
5. All existing screen-switching state replaced by routes.

## Route table

Define routes in one place. Use this shape, adjusting names to the screens that
actually exist in the codebase:

```ts
export const routes = [
  { path: '/',                    component: Dashboard },
  { path: '/links',               component: LinkList },
  { path: '/links/new',           component: LinkCreate },
  { path: '/links/:id',           component: LinkDetail },
  { path: '/campaigns',           component: CampaignList },
  { path: '/campaigns/:id',       component: CampaignDetail },
  { path: '/settings',            component: Settings },
  { path: '*',                    component: NotFound },
] as const;
```

Rules:

- Match in declaration order, first match wins. `*` is last.
- `:param` matches a single path segment and lands in `params`.
- Strip a trailing slash before matching, except for `/`.
- Query values after the hash (`#/links?page=2`) parse into `query` via
  `URLSearchParams`. Do not use `window.location.search` for SPA state, it
  belongs to the outer document.

## Router implementation notes

Use Svelte 5 runes, not stores from `svelte/store`. The project is on Svelte 5
and mixing idioms is the main source of churn here.

```ts
// router.svelte.ts
let current = $state(parse(window.location.hash));

window.addEventListener('hashchange', () => {
  current = parse(window.location.hash);
});
```

Expose a `getRoute()` accessor (or a `route` object with a getter) rather than
exporting the `$state` variable directly, so reactivity survives the module
boundary.

`parse(hash)` should:

- Drop the leading `#`.
- Default an empty or missing hash to `/`.
- Split path from query on the first `?`.
- Return `{ path, params, query, component }`.

Navigation helpers:

- `navigate(path)` sets `window.location.hash = path`. This pushes a history
  entry, which is what we want for normal screen changes.
- `replace(path)` uses `window.location.replace('#' + path)` so no history entry
  is added. Use this for redirects, such as bouncing an unauthenticated user or
  normalizing a malformed hash.
- Do not call `navigate` from inside a component's initialization path without
  guarding it, or you will get a redirect loop.

## Link component

Every in-app navigation must go through `Link.svelte`, never a bare
`<a href="/campaigns">`. The component renders a real anchor with
`href={'#' + to}` so middle-click, cmd-click, and "copy link address" all behave
correctly. Do not use a `<button>` with an `onclick` handler for navigation.

Add an `active` class when the current path matches `to`, exactly for `/` and by
prefix for everything else.

## Initial load

On first load the hash may be empty. Normalize it with `replace('/')` so the URL
is always well formed, then render. Do this before the first render if possible
to avoid a flash of the wrong screen.

If the app has an auth gate, check auth first and `replace` to the login route
on failure, preserving the intended destination so the user can be sent back
after signing in.

## Server-side changes

Only one, and only if it is not already the behavior: the Go handler serving the
embedded SPA must return `index.html` for the admin entry path. Since the
fragment never reaches the server, no wildcard or catch-all is needed. Verify
this before changing anything. If it already works, change nothing in Go.

## Migration of existing screen state

Find every place the app currently tracks "which screen am I on" in component
state and remove it. Symptoms to look for: a `view` or `page` variable in
`App.svelte`, conditional blocks keyed off it, and prop drilling of a setter
function. All of that becomes route data.

Do not leave a parallel source of truth. If a screen needs to remember scroll
position or a filter, that can stay local, but the identity of the screen comes
only from the hash.

## Testing checklist

Work through all of these manually before calling it done:

- Load `#/campaigns/42` directly in a fresh tab. Correct screen renders.
- Click through three screens, then press back three times. Each step goes back
  one screen and the UI updates.
- Press forward. It returns correctly.
- Reload on any deep route. Same screen comes back.
- Cmd-click a `Link`. Opens in a new tab on the right screen.
- Copy a link address from a `Link` and paste it. It includes the `#`.
- Enter a nonsense hash like `#/nope`. The NotFound screen renders and the app
  does not crash or loop.
- Confirm `go.sstools.co/u/{key}` still redirects. This is the regression that
  matters most.
- Confirm no console errors about state being mutated outside an effect.

## Out of scope

- Nested or layout routes.
- Route transition animations.
- Scroll restoration.
- Migrating to the History API or the Navigation API. If that is wanted later it
  is a separate change and will need an Apache rewrite plus a Go catch-all.
