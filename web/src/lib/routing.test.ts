// Unit tests for the pure hash-routing logic (#0120): hash parsing, route
// matching, the Link.svelte "active" rule, and the login redirect-target
// encode/decode round trip. hashRouter.svelte.ts (the runes wiring around these
// functions) is deliberately thin and untested directly — everything with a
// decision to get right lives here.

import { describe, it, expect } from 'vitest';
import {
  parseHash,
  matchRoute,
  isActiveLink,
  loginPathWithRedirect,
  redirectTargetFrom,
  REDIRECT_PARAM,
  type RouteDef,
} from './routing';

describe('parseHash', () => {
  it('defaults an empty hash to /', () => {
    expect(parseHash('').path).toBe('/');
  });

  it('defaults a bare # to /', () => {
    expect(parseHash('#').path).toBe('/');
  });

  it('drops the leading #', () => {
    expect(parseHash('#/campaigns').path).toBe('/campaigns');
  });

  it('strips a trailing slash except for the root', () => {
    expect(parseHash('#/campaigns/').path).toBe('/campaigns');
    expect(parseHash('#/').path).toBe('/');
  });

  it('splits path from query on the first ?', () => {
    const { path, query } = parseHash('#/links?page=2');
    expect(path).toBe('/links');
    expect(query.get('page')).toBe('2');
  });

  it('handles a ? with no path before it', () => {
    const { path, query } = parseHash('#?foo=bar');
    expect(path).toBe('/');
    expect(query.get('foo')).toBe('bar');
  });

  it('returns an empty query when there is no ?', () => {
    const { query } = parseHash('#/campaigns');
    expect([...query.keys()]).toHaveLength(0);
  });

  it('only splits on the FIRST ? (a value may itself contain one)', () => {
    const { path, query } = parseHash('#/links?redirect=%2Flinks%3Ffoo');
    expect(path).toBe('/links');
    expect(query.get('redirect')).toBe('/links?foo');
  });
});

describe('matchRoute', () => {
  const dashboard = { name: 'Dashboard' };
  const linkDetail = { name: 'LinkDetail' };
  const campaigns = { name: 'CampaignsList' };
  const campaignDetail = { name: 'CampaignDetail' };
  const notFound = { name: 'NotFound' };

  const routes: RouteDef<typeof dashboard>[] = [
    { path: '/', component: dashboard },
    { path: '/links/:key', component: linkDetail },
    { path: '/campaigns', component: campaigns },
    { path: '/campaigns/:slug', component: campaignDetail },
    { path: '*', component: notFound },
  ];

  it('matches the root path exactly', () => {
    expect(matchRoute('/', routes)?.component).toBe(dashboard);
  });

  it('matches a static path in declaration order (first match wins)', () => {
    expect(matchRoute('/campaigns', routes)?.component).toBe(campaigns);
  });

  it('extracts a single :param segment', () => {
    const match = matchRoute('/links/solder0903', routes);
    expect(match?.component).toBe(linkDetail);
    expect(match?.params).toEqual({ key: 'solder0903' });
  });

  it('extracts a :param even when a static route of the same prefix exists', () => {
    const match = matchRoute('/campaigns/spring-sale', routes);
    expect(match?.component).toBe(campaignDetail);
    expect(match?.params).toEqual({ slug: 'spring-sale' });
  });

  it('decodes a URI-encoded :param segment', () => {
    const match = matchRoute('/links/a%20b', routes);
    expect(match?.params).toEqual({ key: 'a b' });
  });

  it('requires an exact segment count — no partial/prefix match on a static route', () => {
    expect(matchRoute('/campaigns/spring-sale/extra', routes)?.component).toBe(notFound);
  });

  it('falls through to the catch-all "*" route for an unknown path', () => {
    expect(matchRoute('/nope', routes)?.component).toBe(notFound);
  });

  it('returns null when there is no catch-all and nothing matches', () => {
    const noCatchAll = routes.slice(0, -1);
    expect(matchRoute('/nope', noCatchAll)).toBeNull();
  });

  it('does not throw on a malformed percent-encoded :param segment (a lone %)', () => {
    expect(() => matchRoute('/campaigns/100%', routes)).not.toThrow();
    expect(matchRoute('/campaigns/100%', routes)?.component).toBe(notFound);
  });

  it('does not throw on a truncated multi-byte percent-encoded :param segment', () => {
    expect(() => matchRoute('/links/%E0%A4%A', routes)).not.toThrow();
    expect(matchRoute('/links/%E0%A4%A', routes)?.component).toBe(notFound);
  });
});

describe('isActiveLink', () => {
  it('is exact-match only for the root path', () => {
    expect(isActiveLink('/', '/')).toBe(true);
    expect(isActiveLink('/campaigns', '/')).toBe(false);
    expect(isActiveLink('/links/abc', '/')).toBe(false);
  });

  it('is a prefix match for every other path', () => {
    expect(isActiveLink('/campaigns', '/campaigns')).toBe(true);
    expect(isActiveLink('/campaigns/spring-sale', '/campaigns')).toBe(true);
    expect(isActiveLink('/campaignsx', '/campaigns')).toBe(false);
  });

  it('does not treat an unrelated path as active', () => {
    expect(isActiveLink('/account', '/campaigns')).toBe(false);
  });
});

describe('login redirect-target encoding', () => {
  it('round-trips an ordinary deep path', () => {
    const loginPath = loginPathWithRedirect('/links/solder0903');
    expect(loginPath).toBe('/login?redirect=%2Flinks%2Fsolder0903');
    const [, queryString] = loginPath.split('?');
    expect(redirectTargetFrom(new URLSearchParams(queryString))).toBe('/links/solder0903');
  });

  it('round-trips a path containing reserved characters', () => {
    const loginPath = loginPathWithRedirect('/campaigns/spring sale & co');
    const [, queryString] = loginPath.split('?');
    expect(redirectTargetFrom(new URLSearchParams(queryString))).toBe('/campaigns/spring sale & co');
  });

  it('omits the redirect param entirely for the root path', () => {
    expect(loginPathWithRedirect('/')).toBe('/login');
  });

  it('omits the redirect param for an empty path', () => {
    expect(loginPathWithRedirect('')).toBe('/login');
  });

  it('does not redirect to itself', () => {
    expect(loginPathWithRedirect('/login')).toBe('/login');
  });

  it('defaults to / when there is no redirect param', () => {
    expect(redirectTargetFrom(new URLSearchParams(''))).toBe('/');
  });

  it('defaults to / for a malformed percent-encoding', () => {
    expect(redirectTargetFrom(new URLSearchParams(`${REDIRECT_PARAM}=%`))).toBe('/');
  });

  it('defaults to / for a value that is not an in-app path', () => {
    const q = new URLSearchParams();
    q.set(REDIRECT_PARAM, 'https://evil.example.com');
    expect(redirectTargetFrom(q)).toBe('/');
  });

  it('rejects a protocol-relative //host redirect', () => {
    const q = new URLSearchParams();
    q.set(REDIRECT_PARAM, '//evil.example.com');
    expect(redirectTargetFrom(q)).toBe('/');
  });

  it('rejects a backslash-variant /\\host redirect', () => {
    const q = new URLSearchParams();
    q.set(REDIRECT_PARAM, '/\\evil.example.com');
    expect(redirectTargetFrom(q)).toBe('/');
  });

  it('does not double-decode a target whose original path contained a literal %', () => {
    // loginPathWithRedirect encodes '/links/100%25off' (the literal path
    // '/links/100%off') to '%2Flinks%2F100%2525off'. URLSearchParams.get
    // decodes that ONCE, back to '/links/100%25off' — redirectTargetFrom
    // must not decode it a second time (which would yield '/links/100%off',
    // silently changing the target).
    const loginPath = loginPathWithRedirect('/links/100%25off');
    const [, queryString] = loginPath.split('?');
    expect(redirectTargetFrom(new URLSearchParams(queryString))).toBe('/links/100%25off');
  });
});
