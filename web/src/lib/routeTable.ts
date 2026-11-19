// The hash-route table (#0120), kept out of App.svelte so main.ts can match the
// first route before mounting (#0186). Sign-in and NotFound are eager; every
// other view is a lazy chunk named through routeViews.lazy().
//
// hashRouter.svelte.ts stays free of any view import so routing.ts's matching
// can be unit-tested with no Svelte runtime. Match in declaration order; '*'
// (NotFound) goes last.
import type { RouteDef } from './routing';
import { lazy } from './routeViews';
import Login from '../views/Login.svelte';
import NotFound from '../views/NotFound.svelte';

export const routes: RouteDef<unknown>[] = [
  { path: '/', component: lazy('overview') },
  { path: '/login', component: Login },
  { path: '/links', component: lazy('links') },
  { path: '/links/:key', component: lazy('link-detail') },
  // '/new', not '/links/new': 'new' is a legal custom key, so /links/new
  // must stay the detail page of the link whose key is 'new' (#0153).
  { path: '/new', component: lazy('new-link') },
  { path: '/campaigns', component: lazy('campaigns') },
  { path: '/campaigns/:slug', component: lazy('campaign-detail') },
  { path: '/account', component: lazy('account') },
  // Bare /admin redirects to /admin/settings inside the view (#0154).
  { path: '/admin', component: lazy('admin') },
  { path: '/admin/:section', component: lazy('admin') },
  { path: '*', component: NotFound },
];
